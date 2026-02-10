package store

import (
	"sync"
	"testing"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

func newTestTransaction(id string, status domain.TransactionStatus, category domain.DeclineCategory) *domain.Transaction {
	return &domain.Transaction{
		ID:              id,
		Amount:          100.00,
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
	if got.Amount != 100.00 {
		t.Errorf("expected amount 100.00, got %f", got.Amount)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := New()
	_, err := s.Get("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent transaction")
	}
}

func TestStore_DeepCopy(t *testing.T) {
	s := New()
	tx := newTestTransaction("txn_copy", domain.StatusScheduled, domain.SoftDecline)
	tx.RetryAttempts = []domain.RetryAttempt{
		{AttemptNumber: 1, Success: false},
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

	rejected := s.List("rejected")
	if len(rejected) != 1 {
		t.Errorf("expected 1 rejected, got %d", len(rejected))
	}
}

func TestStore_GetPendingRetries(t *testing.T) {
	s := New()
	s.Save(newTestTransaction("txn_1", domain.StatusScheduled, domain.SoftDecline))
	s.Save(newTestTransaction("txn_2", domain.StatusRetrying, domain.SoftDecline))
	s.Save(newTestTransaction("txn_3", domain.StatusRecovered, domain.SoftDecline))
	s.Save(newTestTransaction("txn_4", domain.StatusFailedFinal, domain.SoftDecline))
	s.Save(newTestTransaction("txn_5", domain.StatusRejected, domain.HardDecline))

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
	if s.Count() != 0 {
		t.Error("expected 0 count for empty store")
	}

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
		}(i)
	}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.GetAll()
			_ = s.GetPendingRetries()
			_ = s.Count()
		}()
	}

	wg.Wait()
}
