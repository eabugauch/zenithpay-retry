package retry

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

func setupEngine() (*Engine, *store.Store, *webhook.Notifier) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := store.New()
	sim := NewSimulator(42) // Fixed seed for deterministic tests
	notifier := webhook.NewNotifier(logger)
	engine := NewEngine(s, sim, notifier, logger)
	return engine, s, notifier
}

func TestSubmit_HardDecline(t *testing.T) {
	engine, s, _ := setupEngine()

	resp, err := engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_hard_001",
		AmountCents:       10000,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "stolen_card",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.DeclineCategory != domain.HardDecline {
		t.Errorf("expected hard decline, got %s", resp.DeclineCategory)
	}
	if resp.RetryEligible {
		t.Error("hard decline should not be retry eligible")
	}
	if resp.Status != domain.StatusRejected {
		t.Errorf("expected rejected status, got %s", resp.Status)
	}
	if resp.RetryPlan != nil {
		t.Error("hard decline should have no retry plan")
	}

	tx, _ := s.Get("txn_hard_001")
	if tx.Status != domain.StatusRejected {
		t.Errorf("stored status should be rejected, got %s", tx.Status)
	}
}

func TestSubmit_SoftDecline(t *testing.T) {
	engine, s, _ := setupEngine()

	resp, err := engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_soft_001",
		AmountCents:       25000,
		Currency:          "BRL",
		CustomerID:        "cust_002",
		OriginalProcessor: "dlocal_br",
		DeclineCode:       "insufficient_funds",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.DeclineCategory != domain.SoftDecline {
		t.Errorf("expected soft decline, got %s", resp.DeclineCategory)
	}
	if !resp.RetryEligible {
		t.Error("soft decline should be retry eligible")
	}
	if resp.Status != domain.StatusScheduled {
		t.Errorf("expected scheduled status, got %s", resp.Status)
	}
	if resp.RetryPlan == nil {
		t.Fatal("soft decline should have a retry plan")
	}
	if resp.RetryPlan.MaxAttempts != 3 {
		t.Errorf("expected 3 max attempts, got %d", resp.RetryPlan.MaxAttempts)
	}

	tx, _ := s.Get("txn_soft_001")
	if tx.NextRetryAt == nil {
		t.Error("expected next retry time to be set")
	}
}

func TestSubmit_DuplicateRejected(t *testing.T) {
	engine, _, _ := setupEngine()

	req := domain.SubmitRequest{
		TransactionID:     "txn_dup_001",
		AmountCents:       10000,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "insufficient_funds",
	}

	_, err := engine.Submit(req)
	if err != nil {
		t.Fatalf("first submit should succeed: %v", err)
	}

	_, err = engine.Submit(req)
	if err == nil {
		t.Error("expected error for duplicate transaction")
	}
}

func TestSubmit_EmitsScheduledWebhook(t *testing.T) {
	engine, _, notifier := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_webhook_001",
		AmountCents:       10000,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "insufficient_funds",
	})

	events := notifier.GetEventsByTransaction("txn_webhook_001")
	if len(events) == 0 {
		t.Fatal("expected at least one webhook event after submit")
	}
	if events[0].EventType != domain.EventRetryScheduled {
		t.Errorf("expected retry.scheduled event, got %s", events[0].EventType)
	}
}

func TestExecuteRetry(t *testing.T) {
	engine, s, _ := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_retry_001",
		AmountCents:       50000,
		Currency:          "MXN",
		CustomerID:        "cust_003",
		OriginalProcessor: "payu_mx",
		DeclineCode:       "issuer_timeout",
	})

	err := engine.ExecuteRetry("txn_retry_001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tx, _ := s.Get("txn_retry_001")
	if len(tx.RetryAttempts) != 1 {
		t.Errorf("expected 1 retry attempt, got %d", len(tx.RetryAttempts))
	}
	if tx.Status != domain.StatusRecovered && tx.Status != domain.StatusRetrying {
		t.Errorf("expected recovered or retrying status, got %s", tx.Status)
	}
}

func TestExecuteRetry_NonExistentTransaction(t *testing.T) {
	engine, _, _ := setupEngine()
	err := engine.ExecuteRetry("ghost_txn")
	if err == nil {
		t.Fatal("expected error for non-existent transaction")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound sentinel, got %v", err)
	}
}

func TestExecuteRetry_HardDeclineNotRetryable(t *testing.T) {
	engine, _, _ := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_hard_retry",
		AmountCents:       10000,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "fraud_suspected",
	})

	err := engine.ExecuteRetry("txn_hard_retry")
	if err == nil {
		t.Fatal("expected error when retrying hard decline")
	}
	if !errors.Is(err, ErrNotRetryable) {
		t.Errorf("expected ErrNotRetryable sentinel, got %v", err)
	}
}

func TestExecuteRetry_ExhaustsAllAttempts(t *testing.T) {
	engine, s, _ := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_exhaust",
		AmountCents:       10000,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "authentication_failed", // max 2 attempts
	})

	for i := 0; i < 5; i++ {
		engine.ExecuteRetry("txn_exhaust")
	}

	tx, _ := s.Get("txn_exhaust")
	if tx.Status != domain.StatusFailedFinal && tx.Status != domain.StatusRecovered {
		t.Errorf("expected terminal status, got %s", tx.Status)
	}
	if tx.NextRetryAt != nil && tx.Status == domain.StatusFailedFinal {
		t.Error("exhausted transaction should have nil NextRetryAt")
	}
}

func TestExecuteRetry_ExhaustedReturnsSentinel(t *testing.T) {
	engine, _, _ := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_exhaust_sentinel",
		AmountCents:       10000,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "authentication_failed", // max 2 attempts
	})

	// Execute all attempts
	var lastErr error
	for i := 0; i < 5; i++ {
		if err := engine.ExecuteRetry("txn_exhaust_sentinel"); err != nil {
			lastErr = err
		}
	}

	if lastErr == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// Should be either ErrAttemptsExhausted or ErrNotRetryable (if already terminal)
	if !errors.Is(lastErr, ErrAttemptsExhausted) && !errors.Is(lastErr, ErrNotRetryable) {
		t.Errorf("expected ErrAttemptsExhausted or ErrNotRetryable, got %v", lastErr)
	}
}

func TestExecuteRetry_WebhookEvents(t *testing.T) {
	engine, _, notifier := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_events",
		AmountCents:       10000,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "authentication_failed",
	})

	for i := 0; i < 3; i++ {
		engine.ExecuteRetry("txn_events")
	}

	events := notifier.GetEventsByTransaction("txn_events")
	if len(events) < 2 {
		t.Errorf("expected at least 2 webhook events (scheduled + retry result), got %d", len(events))
	}

	if events[0].EventType != domain.EventRetryScheduled {
		t.Errorf("first event should be retry.scheduled, got %s", events[0].EventType)
	}
}

func TestProcessAllPending(t *testing.T) {
	engine, s, _ := setupEngine()

	softCodes := []string{"insufficient_funds", "issuer_timeout", "processor_error"}
	for i, code := range softCodes {
		_, _ = engine.Submit(domain.SubmitRequest{
			TransactionID:     fmt.Sprintf("txn_batch_%03d", i+1),
			AmountCents:       int64((i + 1) * 10000),
			Currency:          "USD",
			CustomerID:        fmt.Sprintf("cust_%03d", i+1),
			OriginalProcessor: "stripe_latam",
			DeclineCode:       code,
		})
	}

	processed, recovered := engine.ProcessAllPending()
	if processed == 0 {
		t.Error("expected at least one retry attempt processed")
	}
	if recovered < 0 {
		t.Error("recovered count should be non-negative")
	}

	for i := range softCodes {
		tx, _ := s.Get(fmt.Sprintf("txn_batch_%03d", i+1))
		if tx.Status == domain.StatusScheduled {
			t.Errorf("transaction %s should not still be scheduled", tx.ID)
		}
	}
}
