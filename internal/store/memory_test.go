package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

func newTestTransaction(id string, status domain.TransactionStatus, category domain.DeclineCategory) *domain.Transaction {
	return &domain.Transaction{
		ID:              id,
		AmountCents:     29999,
		Currency:        "USD",
		CustomerID:      "cust_001",
		DeclineCode:     "insufficient_funds",
		DeclineCategory: category,
		Status:          status,
		RetryAttempts:   []domain.RetryAttempt{},
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
}

func TestStore_SaveAndGet(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_001", domain.StatusScheduled, domain.SoftDecline)
	s.Save(tx)

	got, err := s.Get("txn_001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "txn_001" {
		t.Errorf("expected ID txn_001, got %s", got.ID)
	}
	if got.AmountCents != 29999 {
		t.Errorf("expected 29999 cents, got %d", got.AmountCents)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := New()
	_, err := s.Get("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound sentinel, got %v", err)
	}
}

func TestStore_SaveIfNotExists(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_atomic", domain.StatusScheduled, domain.SoftDecline)

	if err := s.SaveIfNotExists(tx); err != nil {
		t.Fatalf("first save should succeed: %v", err)
	}

	err := s.SaveIfNotExists(tx)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}

	// Verify the original was stored correctly
	got, _ := s.Get("txn_atomic")
	if got.AmountCents != 29999 {
		t.Errorf("expected 29999 cents, got %d", got.AmountCents)
	}
}

func TestStore_UpdateFunc(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_update", domain.StatusScheduled, domain.SoftDecline)
	s.Save(tx)

	err := s.UpdateFunc("txn_update", func(tx *domain.Transaction) error {
		tx.Status = domain.StatusRecovered
		tx.RetryAttempts = append(tx.RetryAttempts, domain.RetryAttempt{
			AttemptNumber: 1, Success: true,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := s.Get("txn_update")
	if got.Status != domain.StatusRecovered {
		t.Errorf("expected recovered, got %s", got.Status)
	}
	if len(got.RetryAttempts) != 1 {
		t.Errorf("expected 1 attempt, got %d", len(got.RetryAttempts))
	}
}

func TestStore_UpdateFunc_NotFound(t *testing.T) {
	s := New()
	err := s.UpdateFunc("ghost", func(tx *domain.Transaction) error {
		return nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStore_UpdateFunc_RollbackOnError(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_rollback", domain.StatusScheduled, domain.SoftDecline)
	s.Save(tx)

	testErr := errors.New("test rollback")
	err := s.UpdateFunc("txn_rollback", func(tx *domain.Transaction) error {
		tx.Status = domain.StatusRecovered // should not persist
		return testErr
	})
	if !errors.Is(err, testErr) {
		t.Errorf("expected testErr, got %v", err)
	}

	got, _ := s.Get("txn_rollback")
	if got.Status != domain.StatusScheduled {
		t.Errorf("callback error should rollback, got status %s", got.Status)
	}
}

func TestStore_DeepCopy(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_copy", domain.StatusScheduled, domain.SoftDecline)
	tx.RetryAttempts = []domain.RetryAttempt{
		{AttemptNumber: 1, Success: false},
	}
	next := time.Now().UTC().Add(time.Hour)
	tx.NextRetryAt = &next
	tx.RetryPlan = &domain.RetryPlan{
		MaxAttempts:    3,
		ScheduledTimes: []time.Time{next},
		Processors:     []string{"stripe"},
	}
	s.Save(tx)

	got, _ := s.Get("txn_copy")
	got.RetryAttempts = append(got.RetryAttempts, domain.RetryAttempt{AttemptNumber: 2, Success: true})
	got.Status = domain.StatusRecovered

	original, _ := s.Get("txn_copy")
	if len(original.RetryAttempts) != 1 {
		t.Errorf("store mutation leaked: expected 1 attempt, got %d", len(original.RetryAttempts))
	}
	if original.Status != domain.StatusScheduled {
		t.Errorf("store mutation leaked: expected scheduled, got %s", original.Status)
	}
}

func TestStore_Exists(t *testing.T) {
	s := New()
	if s.Exists("txn_001") {
		t.Error("should not exist before save")
	}
	s.Save(newTestTransaction("txn_001", domain.StatusScheduled, domain.SoftDecline))
	if !s.Exists("txn_001") {
		t.Error("should exist after save")
	}
}

func TestStore_ListFilterByStatus(t *testing.T) {
	s := New()
	s.Save(newTestTransaction("txn_1", domain.StatusScheduled, domain.SoftDecline))
	s.Save(newTestTransaction("txn_2", domain.StatusRecovered, domain.SoftDecline))
	s.Save(newTestTransaction("txn_3", domain.StatusFailedFinal, domain.SoftDecline))
	s.Save(newTestTransaction("txn_4", domain.StatusRejected, domain.HardDecline))

	all := s.List("")
	if len(all) != 4 {
		t.Errorf("expected 4, got %d", len(all))
	}

	recovered := s.List("recovered")
	if len(recovered) != 1 {
		t.Errorf("expected 1 recovered, got %d", len(recovered))
	}
}

func TestStore_GetPendingRetries(t *testing.T) {
	s := New()
	s.Save(newTestTransaction("txn_1", domain.StatusScheduled, domain.SoftDecline))
	s.Save(newTestTransaction("txn_2", domain.StatusRetrying, domain.SoftDecline))
	s.Save(newTestTransaction("txn_3", domain.StatusRecovered, domain.SoftDecline))
	s.Save(newTestTransaction("txn_4", domain.StatusRejected, domain.HardDecline))

	pending := s.GetPendingRetries()
	if len(pending) != 2 {
		t.Errorf("expected 2 pending retries, got %d", len(pending))
	}
}

func TestStore_GetAllSoftDeclines(t *testing.T) {
	s := New()
	s.Save(newTestTransaction("txn_1", domain.StatusScheduled, domain.SoftDecline))
	s.Save(newTestTransaction("txn_2", domain.StatusRejected, domain.HardDecline))
	s.Save(newTestTransaction("txn_3", domain.StatusRecovered, domain.SoftDecline))

	soft := s.GetAllSoftDeclines()
	if len(soft) != 2 {
		t.Errorf("expected 2 soft declines, got %d", len(soft))
	}
}

func TestStore_CountAndClear(t *testing.T) {
	s := New()
	s.Save(newTestTransaction("txn_1", domain.StatusScheduled, domain.SoftDecline))
	s.Save(newTestTransaction("txn_2", domain.StatusScheduled, domain.SoftDecline))
	if s.Count() != 2 {
		t.Errorf("expected 2, got %d", s.Count())
	}
	s.Clear()
	if s.Count() != 0 {
		t.Errorf("expected 0 after clear, got %d", s.Count())
	}
}

func TestStore_PendingIndex_ConsistentAfterUpdate(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_idx", domain.StatusScheduled, domain.SoftDecline)
	s.Save(tx)

	// Should be in pending set
	if len(s.GetPendingRetries()) != 1 {
		t.Fatal("expected 1 pending after save")
	}

	// Transition to recovered via UpdateFunc
	s.UpdateFunc("txn_idx", func(tx *domain.Transaction) error {
		tx.Status = domain.StatusRecovered
		return nil
	})

	// Should be removed from pending set
	if len(s.GetPendingRetries()) != 0 {
		t.Error("expected 0 pending after recovery")
	}

	// Transition back to retrying
	s.UpdateFunc("txn_idx", func(tx *domain.Transaction) error {
		tx.Status = domain.StatusRetrying
		return nil
	})

	if len(s.GetPendingRetries()) != 1 {
		t.Error("expected 1 pending after re-entering retrying state")
	}
}

func TestStore_GetDueRetries(t *testing.T) {
	s := New()
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	future := now.Add(24 * time.Hour)

	// Due: scheduled with past NextRetryAt
	tx1 := newTestTransaction("txn_due", domain.StatusScheduled, domain.SoftDecline)
	tx1.NextRetryAt = &past
	s.Save(tx1)

	// Not due: scheduled with future NextRetryAt
	tx2 := newTestTransaction("txn_future", domain.StatusScheduled, domain.SoftDecline)
	tx2.NextRetryAt = &future
	s.Save(tx2)

	// Not due: nil NextRetryAt
	tx3 := newTestTransaction("txn_nil", domain.StatusScheduled, domain.SoftDecline)
	s.Save(tx3)

	// Not pending: recovered with past NextRetryAt
	tx4 := newTestTransaction("txn_terminal", domain.StatusRecovered, domain.SoftDecline)
	tx4.NextRetryAt = &past
	s.Save(tx4)

	due := s.GetDueRetries(now)
	if len(due) != 1 {
		t.Fatalf("expected 1 due retry, got %d", len(due))
	}
	if due[0].ID != "txn_due" {
		t.Errorf("expected txn_due, got %s", due[0].ID)
	}
}

func TestStore_PendingIndex_SaveIfNotExists(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_sne", domain.StatusScheduled, domain.SoftDecline)

	if err := s.SaveIfNotExists(tx); err != nil {
		t.Fatal(err)
	}
	if len(s.GetPendingRetries()) != 1 {
		t.Error("SaveIfNotExists should add to pending index")
	}

	// Non-pending status should not appear in index
	tx2 := newTestTransaction("txn_rejected", domain.StatusRejected, domain.HardDecline)
	s.SaveIfNotExists(tx2)
	if len(s.GetPendingRetries()) != 1 {
		t.Error("rejected transaction should not be in pending index")
	}
}

func TestStore_PendingIndex_Clear(t *testing.T) {
	s := New()
	s.Save(newTestTransaction("txn_1", domain.StatusScheduled, domain.SoftDecline))
	s.Save(newTestTransaction("txn_2", domain.StatusRetrying, domain.SoftDecline))

	if len(s.GetPendingRetries()) != 2 {
		t.Fatal("expected 2 pending before clear")
	}

	s.Clear()
	if len(s.GetPendingRetries()) != 0 {
		t.Error("expected 0 pending after clear")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := New()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx := newTestTransaction(
				"txn_concurrent_"+string(rune('A'+i%26))+string(rune('0'+i/26)),
				domain.StatusScheduled,
				domain.SoftDecline,
			)
			s.Save(tx)
			s.Get(tx.ID)
			s.List("")
			s.GetPendingRetries()
		}(i)
	}

	wg.Wait()
	if s.Count() == 0 {
		t.Error("store should have entries after concurrent writes")
	}
}
