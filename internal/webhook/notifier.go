package webhook

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

// Notifier simulates sending webhook notifications to merchants.
type Notifier struct {
	mu     sync.Mutex
	events []domain.WebhookEvent
	logger *slog.Logger
}

// NewNotifier creates a new webhook notifier.
func NewNotifier(logger *slog.Logger) *Notifier {
	return &Notifier{
		events: []domain.WebhookEvent{},
		logger: logger,
	}
}

// Send simulates delivering a webhook event to the merchant's endpoint.
func (n *Notifier) Send(tx *domain.Transaction, eventType string, attemptNumber int) {
	event := domain.WebhookEvent{
		EventType:     eventType,
		TransactionID: tx.ID,
		Status:        tx.Status,
		AttemptNumber: attemptNumber,
		Timestamp:     time.Now().UTC(),
	}

	n.mu.Lock()
	n.events = append(n.events, event)
	n.mu.Unlock()

	payload, _ := json.Marshal(event)

	if tx.WebhookURL != "" {
		n.logger.Info("webhook delivered",
			"url", tx.WebhookURL,
			"event_type", eventType,
			"transaction_id", tx.ID,
			"payload", string(payload),
		)
	} else {
		n.logger.Debug("webhook event recorded (no URL configured)",
			"event_type", eventType,
			"transaction_id", tx.ID,
		)
	}
}

// GetEvents returns all recorded webhook events.
func (n *Notifier) GetEvents() []domain.WebhookEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	result := make([]domain.WebhookEvent, len(n.events))
	copy(result, n.events)
	return result
}

// GetEventsByTransaction returns webhook events for a specific transaction.
func (n *Notifier) GetEventsByTransaction(txID string) []domain.WebhookEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	var result []domain.WebhookEvent
	for _, e := range n.events {
		if e.TransactionID == txID {
			result = append(result, e)
		}
	}
	return result
}

// Clear removes all recorded events.
func (n *Notifier) Clear() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = []domain.WebhookEvent{}
}
