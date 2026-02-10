package retry

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

func setupSchedulerTest() (*Scheduler, *store.Store) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := store.New()
	sim := NewSimulator(42)
	notifier := webhook.NewNotifier(logger)
	engine := NewEngine(s, sim, notifier, logger)
	scheduler := NewScheduler(engine, s, 50*time.Millisecond, logger)
	return scheduler, s
}

func TestScheduler_StopsOnContextCancel(t *testing.T) {
	scheduler, _ := setupSchedulerTest()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		scheduler.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Scheduler exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after context cancellation")
	}
}

func TestScheduler_ExecutesDueRetries(t *testing.T) {
	scheduler, s := setupSchedulerTest()

	// Create a transaction with NextRetryAt in the past (due now)
	past := time.Now().UTC().Add(-1 * time.Hour)
	tx := &domain.Transaction{
		ID:              "txn_due",
		AmountCents:     10000,
		Currency:        "USD",
		DeclineCode:     "issuer_timeout",
		DeclineCategory: domain.SoftDecline,
		Status:          domain.StatusScheduled,
		NextRetryAt:     &past,
		RetryAttempts:   []domain.RetryAttempt{},
		RetryPlan: &domain.RetryPlan{
			MaxAttempts:    3,
			DeclineCode:    "issuer_timeout",
			ScheduledTimes: []time.Time{past, past.Add(5 * time.Minute), past.Add(30 * time.Minute)},
			Processors:     []string{"stripe_latam", "adyen_apac", "dlocal_br"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	s.Save(tx)

	ctx, cancel := context.WithCancel(context.Background())
	go scheduler.Start(ctx)

	// Wait for at least one scheduler tick
	time.Sleep(200 * time.Millisecond)
	cancel()

	got, _ := s.Get("txn_due")
	if len(got.RetryAttempts) == 0 {
		t.Error("scheduler should have executed the due retry")
	}
}

func TestScheduler_SkipsNotYetDue(t *testing.T) {
	scheduler, s := setupSchedulerTest()

	// Create a transaction with NextRetryAt far in the future
	future := time.Now().UTC().Add(24 * time.Hour)
	tx := &domain.Transaction{
		ID:              "txn_future",
		AmountCents:     10000,
		Currency:        "USD",
		DeclineCode:     "insufficient_funds",
		DeclineCategory: domain.SoftDecline,
		Status:          domain.StatusScheduled,
		NextRetryAt:     &future,
		RetryAttempts:   []domain.RetryAttempt{},
		RetryPlan: &domain.RetryPlan{
			MaxAttempts:    3,
			DeclineCode:    "insufficient_funds",
			ScheduledTimes: []time.Time{future},
			Processors:     []string{"stripe_latam"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	s.Save(tx)

	ctx, cancel := context.WithCancel(context.Background())
	go scheduler.Start(ctx)

	time.Sleep(200 * time.Millisecond)
	cancel()

	got, _ := s.Get("txn_future")
	if len(got.RetryAttempts) != 0 {
		t.Error("scheduler should not execute retries that are not yet due")
	}
}

func TestScheduler_SkipsNilNextRetryAt(t *testing.T) {
	scheduler, s := setupSchedulerTest()

	tx := &domain.Transaction{
		ID:              "txn_nil_retry",
		AmountCents:     10000,
		Currency:        "USD",
		DeclineCode:     "insufficient_funds",
		DeclineCategory: domain.SoftDecline,
		Status:          domain.StatusScheduled,
		NextRetryAt:     nil, // no retry time set
		RetryAttempts:   []domain.RetryAttempt{},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	s.Save(tx)

	ctx, cancel := context.WithCancel(context.Background())
	go scheduler.Start(ctx)

	time.Sleep(200 * time.Millisecond)
	cancel()

	got, _ := s.Get("txn_nil_retry")
	if len(got.RetryAttempts) != 0 {
		t.Error("scheduler should skip transactions with nil NextRetryAt")
	}
}

func TestScheduler_SkipsTerminalStatus(t *testing.T) {
	scheduler, s := setupSchedulerTest()

	past := time.Now().UTC().Add(-1 * time.Hour)
	tx := &domain.Transaction{
		ID:              "txn_terminal",
		AmountCents:     10000,
		Currency:        "USD",
		DeclineCode:     "insufficient_funds",
		DeclineCategory: domain.SoftDecline,
		Status:          domain.StatusRecovered, // terminal
		NextRetryAt:     &past,
		RetryAttempts:   []domain.RetryAttempt{},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	s.Save(tx)

	ctx, cancel := context.WithCancel(context.Background())
	go scheduler.Start(ctx)

	time.Sleep(200 * time.Millisecond)
	cancel()

	got, _ := s.Get("txn_terminal")
	if len(got.RetryAttempts) != 0 {
		t.Error("scheduler should skip transactions in terminal status")
	}
}
