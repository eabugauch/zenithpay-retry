package retry

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

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
func (e *Engine) Submit(req domain.SubmitRequest) (*domain.SubmitResponse, error) {
	if e.store.Exists(req.TransactionID) {
		return nil, fmt.Errorf("transaction %s already submitted", req.TransactionID)
	}

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
		Amount:            req.Amount,
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
		e.store.Save(tx)
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

	e.store.Save(tx)
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
func (e *Engine) ExecuteRetry(txID string) error {
	tx, err := e.store.Get(txID)
	if err != nil {
		return fmt.Errorf("executing retry for %s: %w", txID, err)
	}

	if tx.Status != domain.StatusScheduled && tx.Status != domain.StatusRetrying {
		return fmt.Errorf("transaction %s is not eligible for retry (status: %s)", txID, tx.Status)
	}

	if tx.RetryPlan == nil {
		return fmt.Errorf("transaction %s has no retry plan", txID)
	}

	attemptNum := len(tx.RetryAttempts) + 1
	if attemptNum > tx.RetryPlan.MaxAttempts {
		tx.Status = domain.StatusFailedFinal
		tx.NextRetryAt = nil
		tx.UpdatedAt = time.Now().UTC()
		e.store.Save(tx)
		e.notifier.Send(tx, domain.EventRetryExhausted, attemptNum-1)
		return fmt.Errorf("transaction %s has exhausted all retry attempts", txID)
	}

	processor := tx.RetryPlan.Processors[attemptNum-1]
	scheduledAt := tx.RetryPlan.ScheduledTimes[attemptNum-1]

	e.logger.Info("executing retry attempt",
		"transaction_id", tx.ID,
		"attempt", attemptNum,
		"processor", processor,
	)

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
	tx.RetryAttempts = append(tx.RetryAttempts, attempt)
	tx.UpdatedAt = time.Now().UTC()

	if result.Success {
		tx.Status = domain.StatusRecovered
		tx.NextRetryAt = nil
		e.store.Save(tx)
		e.notifier.Send(tx, domain.EventRetrySucceeded, attemptNum)
		e.logger.Info("transaction recovered",
			"transaction_id", tx.ID,
			"attempt", attemptNum,
			"processor", processor,
		)
		return nil
	}

	if attemptNum >= tx.RetryPlan.MaxAttempts {
		tx.Status = domain.StatusFailedFinal
		tx.NextRetryAt = nil
		e.store.Save(tx)
		e.notifier.Send(tx, domain.EventRetryExhausted, attemptNum)
		e.logger.Info("transaction failed after all retries",
			"transaction_id", tx.ID,
			"total_attempts", attemptNum,
		)
		return nil
	}

	tx.Status = domain.StatusRetrying
	nextRetry := tx.RetryPlan.ScheduledTimes[attemptNum]
	tx.NextRetryAt = &nextRetry
	e.store.Save(tx)
	e.notifier.Send(tx, domain.EventRetryFailed, attemptNum)
	e.logger.Info("retry attempt failed, next scheduled",
		"transaction_id", tx.ID,
		"attempt", attemptNum,
		"next_retry_at", nextRetry,
	)
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
