package retry

import (
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

func setupEngine() (*Engine, *store.Store) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	s := store.New()
	sim := NewSimulator(42) // Fixed seed for deterministic tests
	notifier := webhook.NewNotifier(logger)
	engine := NewEngine(s, sim, notifier, logger)
	return engine, s
}

func TestSubmit_HardDecline(t *testing.T) {
	engine, s := setupEngine()

	resp, err := engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_hard_001",
		Amount:            100.00,
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
	engine, s := setupEngine()

	resp, err := engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_soft_001",
		Amount:            250.00,
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
	engine, _ := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_dup_001",
		Amount:            100.00,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "insufficient_funds",
	})

	_, err := engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_dup_001",
		Amount:            100.00,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "insufficient_funds",
	})

	if err == nil {
		t.Error("expected error for duplicate transaction")
	}
}

func TestExecuteRetry(t *testing.T) {
	engine, s := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_retry_001",
		Amount:            500.00,
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

func TestExecuteRetry_HardDeclineNotRetryable(t *testing.T) {
	engine, _ := setupEngine()

	_, _ = engine.Submit(domain.SubmitRequest{
		TransactionID:     "txn_hard_retry",
		Amount:            100.00,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "fraud_suspected",
	})

	err := engine.ExecuteRetry("txn_hard_retry")
	if err == nil {
		t.Error("expected error when retrying hard decline")
	}
}

func TestProcessAllPending(t *testing.T) {
	engine, s := setupEngine()

	softCodes := []string{"insufficient_funds", "issuer_timeout", "processor_error"}
	for i, code := range softCodes {
		_, _ = engine.Submit(domain.SubmitRequest{
			TransactionID:     fmt.Sprintf("txn_batch_%03d", i+1),
			Amount:            float64((i + 1) * 100),
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

	// Verify all transactions reached terminal state
	for i := range softCodes {
		tx, _ := s.Get(fmt.Sprintf("txn_batch_%03d", i+1))
		if tx.Status == domain.StatusScheduled {
			t.Errorf("transaction %s should not still be scheduled", tx.ID)
		}
	}

	_ = recovered // recovered count depends on simulator randomness
}
