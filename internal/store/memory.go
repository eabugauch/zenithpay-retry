package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

// ErrNotFound is returned when a transaction does not exist in the store.
var ErrNotFound = errors.New("transaction not found")

// ErrAlreadyExists is returned when a transaction with the same ID already exists.
var ErrAlreadyExists = errors.New("transaction already exists")

// Store provides thread-safe in-memory storage for transactions.
// All read methods return deep copies to prevent data races from
// external mutation of shared pointers.
//
// A secondary index (pendingIDs) tracks transactions in retryable states,
// enabling O(pending) scheduler lookups instead of O(total) full scans.
type Store struct {
	mu           sync.RWMutex
	transactions map[string]*domain.Transaction
	pendingIDs   map[string]struct{} // secondary index: scheduled/retrying transactions
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		transactions: make(map[string]*domain.Transaction),
		pendingIDs:   make(map[string]struct{}),
	}
}

// isPendingStatus returns true if the status represents a retryable state.
func isPendingStatus(status domain.TransactionStatus) bool {
	return status == domain.StatusScheduled || status == domain.StatusRetrying
}

// updatePendingIndex maintains the secondary index after a mutation.
// Must be called with write lock held.
func (s *Store) updatePendingIndex(id string, status domain.TransactionStatus) {
	if isPendingStatus(status) {
		s.pendingIDs[id] = struct{}{}
	} else {
		delete(s.pendingIDs, id)
	}
}

// Save stores or updates a transaction (deep copy on write).
func (s *Store) Save(tx *domain.Transaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactions[tx.ID] = copyTransaction(tx)
	s.updatePendingIndex(tx.ID, tx.Status)
}

// SaveIfNotExists atomically stores a transaction only if no transaction with
// the same ID exists. Returns ErrAlreadyExists if the ID is taken.
// This prevents the TOCTOU race condition of Exists() + Save().
func (s *Store) SaveIfNotExists(tx *domain.Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.transactions[tx.ID]; ok {
		return ErrAlreadyExists
	}
	s.transactions[tx.ID] = copyTransaction(tx)
	s.updatePendingIndex(tx.ID, tx.Status)
	return nil
}

// UpdateFunc atomically reads a transaction, passes it to the callback for mutation,
// and saves the result back. This prevents lost-update race conditions on
// read-modify-write sequences (e.g., concurrent ExecuteRetry calls).
// The callback receives a deep copy; its mutations are saved atomically.
func (s *Store) UpdateFunc(id string, fn func(tx *domain.Transaction) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, ok := s.transactions[id]
	if !ok {
		return ErrNotFound
	}
	cp := copyTransaction(tx)
	if err := fn(cp); err != nil {
		return err
	}
	s.transactions[id] = copyTransaction(cp)
	s.updatePendingIndex(id, cp.Status)
	return nil
}

// Get retrieves a deep copy of a transaction by ID.
func (s *Store) Get(id string) (*domain.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, ok := s.transactions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return copyTransaction(tx), nil
}

// Exists checks if a transaction already exists.
func (s *Store) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.transactions[id]
	return ok
}

// List returns deep copies of all transactions, optionally filtered by status.
func (s *Store) List(status string) []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction
	for _, tx := range s.transactions {
		if status == "" || string(tx.Status) == status {
			result = append(result, copyTransaction(tx))
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

// GetPendingRetries returns deep copies of transactions that are scheduled or retrying.
// Uses the secondary index for O(pending) lookup instead of O(total) full scan.
func (s *Store) GetPendingRetries() []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*domain.Transaction, 0, len(s.pendingIDs))
	for id := range s.pendingIDs {
		if tx, ok := s.transactions[id]; ok {
			result = append(result, copyTransaction(tx))
		}
	}
	return result
}

// GetDueRetries returns pending transactions whose NextRetryAt is at or before the given time.
// Combines the pending index with a time filter, pushing all filtering into the store layer.
func (s *Store) GetDueRetries(before time.Time) []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction
	for id := range s.pendingIDs {
		tx, ok := s.transactions[id]
		if !ok {
			continue
		}
		if tx.NextRetryAt != nil && !before.Before(*tx.NextRetryAt) {
			result = append(result, copyTransaction(tx))
		}
	}
	return result
}

// GetAllSoftDeclines returns deep copies of all soft-declined transactions.
func (s *Store) GetAllSoftDeclines() []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction
	for _, tx := range s.transactions {
		if tx.DeclineCategory == domain.SoftDecline {
			result = append(result, copyTransaction(tx))
		}
	}
	return result
}

// GetAll returns deep copies of all transactions sorted by creation time descending.
func (s *Store) GetAll() []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*domain.Transaction, 0, len(s.transactions))
	for _, tx := range s.transactions {
		result = append(result, copyTransaction(tx))
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

// Count returns the total number of transactions.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.transactions)
}

// Clear removes all transactions (used for testing/reset).
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactions = make(map[string]*domain.Transaction)
	s.pendingIDs = make(map[string]struct{})
}

// copyTransaction creates a deep copy of a transaction to prevent shared pointer mutations.
func copyTransaction(tx *domain.Transaction) *domain.Transaction {
	cp := *tx

	if tx.RetryPlan != nil {
		plan := *tx.RetryPlan
		plan.ScheduledTimes = make([]time.Time, len(tx.RetryPlan.ScheduledTimes))
		copy(plan.ScheduledTimes, tx.RetryPlan.ScheduledTimes)
		plan.Processors = make([]string, len(tx.RetryPlan.Processors))
		copy(plan.Processors, tx.RetryPlan.Processors)
		cp.RetryPlan = &plan
	}

	cp.RetryAttempts = make([]domain.RetryAttempt, len(tx.RetryAttempts))
	copy(cp.RetryAttempts, tx.RetryAttempts)

	if tx.NextRetryAt != nil {
		t := *tx.NextRetryAt
		cp.NextRetryAt = &t
	}

	return &cp
}
