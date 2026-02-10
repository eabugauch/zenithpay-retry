package store

import (
	"fmt"
	"sort"
	"sync"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

// Store provides thread-safe in-memory storage for transactions.
type Store struct {
	mu           sync.RWMutex
	transactions map[string]*domain.Transaction
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		transactions: make(map[string]*domain.Transaction),
	}
}

// Save stores or updates a transaction.
func (s *Store) Save(tx *domain.Transaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transactions[tx.ID] = tx
}

// Get retrieves a transaction by ID.
func (s *Store) Get(id string) (*domain.Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, ok := s.transactions[id]
	if !ok {
		return nil, fmt.Errorf("transaction %s not found", id)
	}
	return tx, nil
}

// Exists checks if a transaction already exists.
func (s *Store) Exists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.transactions[id]
	return ok
}

// List returns all transactions, optionally filtered by status.
func (s *Store) List(status string) []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction
	for _, tx := range s.transactions {
		if status == "" || string(tx.Status) == status {
			result = append(result, tx)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result
}

// GetPendingRetries returns all transactions that are scheduled or retrying
// and have a next retry time that is due.
func (s *Store) GetPendingRetries() []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction
	for _, tx := range s.transactions {
		if tx.Status == domain.StatusScheduled || tx.Status == domain.StatusRetrying {
			result = append(result, tx)
		}
	}
	return result
}

// GetAllSoftDeclines returns all transactions classified as soft declines.
func (s *Store) GetAllSoftDeclines() []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*domain.Transaction
	for _, tx := range s.transactions {
		if tx.DeclineCategory == domain.SoftDecline {
			result = append(result, tx)
		}
	}
	return result
}

// GetAll returns all transactions.
func (s *Store) GetAll() []*domain.Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*domain.Transaction, 0, len(s.transactions))
	for _, tx := range s.transactions {
		result = append(result, tx)
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
}
