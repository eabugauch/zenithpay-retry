package retry

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

// ErrNotRetryable indicates a transaction cannot be retried (hard decline or terminal state).
var ErrNotRetryable = errors.New("transaction is not retryable")

// ErrAttemptsExhausted indicates all retry attempts have been used.
var ErrAttemptsExhausted = errors.New("all retry attempts exhausted")

// Engine orchestrates the retry logic for failed transactions.
type Engine struct {
	store     *store.Store
	simulator *Simulator
	notifier  *webhook.Notifier
	logger    *slog.Logger
}

// NewEngine creates a new retry engine.
func NewEngine(s *store.Store, sim *Simulator, n *webhook.Notifier, logger *slog.Logger) *Engine {
	return &Engine{
		store:     s,
		simulator: sim,
		notifier:  n,
		logger:    logger,
	}
}

// Submit evaluates a failed transaction and creates a retry plan if eligible.
// Uses SaveIfNotExists for atomic idempotency — no TOCTOU race.
func (e *Engine) Submit(req domain.SubmitRequest) (*domain.SubmitResponse, error) {
	category, reason := domain.ClassifyDecline(req.DeclineCode)
	now := time.Now().UTC()

	var parsedTime time.Time
	if req.Timestamp != "" {
		var err error
		parsedTime, err = time.Parse(time.RFC3339, req.Timestamp)
		if err != nil {
			parsedTime = now
		}
	} else {
		parsedTime = now
	}

	tx := &domain.Transaction{
		ID:                req.TransactionID,
		AmountCents:       req.AmountCents,
		Currency:          req.Currency,
		CustomerID:        req.CustomerID,
		MerchantID:        req.MerchantID,
		OriginalProcessor: req.OriginalProcessor,
		DeclineCode:       req.DeclineCode,
		DeclineCategory:   category,
		RetryAttempts:     []domain.RetryAttempt{},
		CreatedAt:         parsedTime,
		UpdatedAt:         now,
		WebhookURL:        req.WebhookURL,
	}

	if category == domain.HardDecline {
		tx.Status = domain.StatusRejected
		if err := e.store.SaveIfNotExists(tx); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				return nil, fmt.Errorf("transaction %s already submitted", req.TransactionID)
			}
			return nil, fmt.Errorf("saving transaction %s: %w", req.TransactionID, err)
		}
		e.logger.Info("hard decline rejected",
			"transaction_id", tx.ID,
			"decline_code", tx.DeclineCode,
			"reason", reason,
		)
		return &domain.SubmitResponse{
			TransactionID:   tx.ID,
			DeclineCategory: category,
			Status:          tx.Status,
			RetryEligible:   false,
			Message:         fmt.Sprintf("Hard decline: %s. Transaction will not be retried.", reason),
		}, nil
	}

	plan := domain.BuildRetryPlan(req.DeclineCode, req.OriginalProcessor, now)
	tx.RetryPlan = plan
	tx.Status = domain.StatusScheduled
	if len(plan.ScheduledTimes) > 0 {
		nextRetry := plan.ScheduledTimes[0]
		tx.NextRetryAt = &nextRetry
	}

	if err := e.store.SaveIfNotExists(tx); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return nil, fmt.Errorf("transaction %s already submitted", req.TransactionID)
		}
		return nil, fmt.Errorf("saving transaction %s: %w", req.TransactionID, err)
	}

	e.notifier.Send(tx, domain.EventRetryScheduled, 0)
	e.logger.Info("transaction scheduled for retry",
		"transaction_id", tx.ID,
		"decline_code", tx.DeclineCode,
		"max_attempts", plan.MaxAttempts,
		"first_retry_at", tx.NextRetryAt,
	)

	return &domain.SubmitResponse{
		TransactionID:   tx.ID,
		DeclineCategory: category,
		Status:          tx.Status,
		RetryEligible:   true,
		RetryPlan:       plan,
		Message:         fmt.Sprintf("Soft decline: %s. Scheduled %d retry attempts.", reason, plan.MaxAttempts),
	}, nil
}

// ExecuteRetry performs the next retry attempt for a transaction.
// Uses UpdateFunc for atomic read-modify-write — no lost-update race.
func (e *Engine) ExecuteRetry(txID string) error {
	// Simulate outside the lock to avoid holding the mutex during I/O.
	// First, read the current state to determine what to simulate.
	tx, err := e.store.Get(txID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("transaction %s not found: %w", txID, store.ErrNotFound)
		}
		return fmt.Errorf("executing retry for %s: %w", txID, err)
	}

	if tx.Status != domain.StatusScheduled && tx.Status != domain.StatusRetrying {
		return fmt.Errorf("transaction %s is not eligible for retry (status: %s): %w", txID, tx.Status, ErrNotRetryable)
	}
	if tx.RetryPlan == nil {
		return fmt.Errorf("transaction %s has no retry plan: %w", txID, ErrNotRetryable)
	}

	attemptNum := len(tx.RetryAttempts) + 1
	if attemptNum > tx.RetryPlan.MaxAttempts {
		// Mark as exhausted atomically
		e.store.UpdateFunc(txID, func(tx *domain.Transaction) error {
			tx.Status = domain.StatusFailedFinal
			tx.NextRetryAt = nil
			tx.UpdatedAt = time.Now().UTC()
			return nil
		})
		e.notifier.Send(tx, domain.EventRetryExhausted, attemptNum-1)
		return fmt.Errorf("transaction %s: %w", txID, ErrAttemptsExhausted)
	}

	processor := tx.RetryPlan.Processors[attemptNum-1]
	scheduledAt := tx.RetryPlan.ScheduledTimes[attemptNum-1]

	e.logger.Info("executing retry attempt",
		"transaction_id", tx.ID,
		"attempt", attemptNum,
		"processor", processor,
	)

	// Simulate payment outside the store lock
	result := e.simulator.ProcessPayment(tx.DeclineCode, attemptNum, processor)

	attempt := domain.RetryAttempt{
		AttemptNumber: attemptNum,
		Processor:     processor,
		ScheduledAt:   scheduledAt,
		ExecutedAt:    time.Now().UTC(),
		Success:       result.Success,
		ResponseCode:  result.ResponseCode,
		ResponseMsg:   result.ResponseMessage,
	}

	// Atomically update the transaction with the retry result
	var finalStatus domain.TransactionStatus
	err = e.store.UpdateFunc(txID, func(tx *domain.Transaction) error {
		// Re-check state inside the lock to handle concurrent retries
		if tx.Status != domain.StatusScheduled && tx.Status != domain.StatusRetrying {
			return fmt.Errorf("concurrent state change: %w", ErrNotRetryable)
		}
		// Re-check attempt count to avoid duplicate attempts
		if len(tx.RetryAttempts)+1 != attemptNum {
			return fmt.Errorf("concurrent retry detected: %w", ErrNotRetryable)
		}

		tx.RetryAttempts = append(tx.RetryAttempts, attempt)
		tx.UpdatedAt = time.Now().UTC()

		if result.Success {
			tx.Status = domain.StatusRecovered
			tx.NextRetryAt = nil
		} else if attemptNum >= tx.RetryPlan.MaxAttempts {
			tx.Status = domain.StatusFailedFinal
			tx.NextRetryAt = nil
		} else {
			tx.Status = domain.StatusRetrying
			nextRetry := tx.RetryPlan.ScheduledTimes[attemptNum]
			tx.NextRetryAt = &nextRetry
		}
		finalStatus = tx.Status
		return nil
	})
	if err != nil {
		return err
	}

	// Send webhook after successful update
	switch finalStatus {
	case domain.StatusRecovered:
		e.notifier.Send(tx, domain.EventRetrySucceeded, attemptNum)
		e.logger.Info("transaction recovered",
			"transaction_id", tx.ID,
			"attempt", attemptNum,
			"processor", processor,
		)
	case domain.StatusFailedFinal:
		e.notifier.Send(tx, domain.EventRetryExhausted, attemptNum)
		e.logger.Info("transaction failed after all retries",
			"transaction_id", tx.ID,
			"total_attempts", attemptNum,
		)
	default:
		e.notifier.Send(tx, domain.EventRetryFailed, attemptNum)
		e.logger.Info("retry attempt failed, next scheduled",
			"transaction_id", tx.ID,
			"attempt", attemptNum,
		)
	}

	return nil
}

// ProcessAllPending executes retries for all due transactions (demo/accelerated mode).
func (e *Engine) ProcessAllPending() (processed int, recovered int) {
	pending := e.store.GetPendingRetries()
	for _, tx := range pending {
		for {
			err := e.ExecuteRetry(tx.ID)
			if err != nil {
				break
			}
			processed++
			refreshed, err := e.store.Get(tx.ID)
			if err != nil {
				break
			}
			if refreshed.Status == domain.StatusRecovered {
				recovered++
				break
			}
			if refreshed.Status == domain.StatusFailedFinal {
				break
			}
		}
	}
	return processed, recovered
}
