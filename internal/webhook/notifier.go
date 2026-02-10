package webhook

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

// Notifier sends webhook notifications to merchants and records all events.
type Notifier struct {
	mu     sync.RWMutex
	events []domain.WebhookEvent
	client *http.Client
	logger *slog.Logger
}

// NewNotifier creates a new webhook notifier with an HTTP client for delivery.
func NewNotifier(logger *slog.Logger) *Notifier {
	return &Notifier{
		events: []domain.WebhookEvent{},
		client: &http.Client{Timeout: 5 * time.Second},
		logger: logger,
	}
}

// Send delivers a webhook event to the merchant's endpoint (if configured)
// and records the event in the internal log.
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

	if tx.WebhookURL != "" {
		go n.deliver(tx.WebhookURL, event)
	} else {
		n.logger.Debug("webhook event recorded (no URL configured)",
			"event_type", eventType,
			"transaction_id", tx.ID,
		)
	}
}

// deliver attempts an HTTP POST to the merchant webhook URL.
func (n *Notifier) deliver(url string, event domain.WebhookEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		n.logger.Error("webhook marshal failed", "error", err)
		return
	}

	resp, err := n.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		n.logger.Warn("webhook delivery failed",
			"url", url,
			"event_type", event.EventType,
			"transaction_id", event.TransactionID,
			"error", err,
		)
		return
	}
	defer resp.Body.Close()

	n.logger.Info("webhook delivered",
		"url", url,
		"event_type", event.EventType,
		"transaction_id", event.TransactionID,
		"status_code", resp.StatusCode,
	)
}

// GetEvents returns all recorded webhook events.
func (n *Notifier) GetEvents() []domain.WebhookEvent {
	n.mu.RLock()
	defer n.mu.RUnlock()
	result := make([]domain.WebhookEvent, len(n.events))
	copy(result, n.events)
	return result
}

// GetEventsByTransaction returns webhook events for a specific transaction.
func (n *Notifier) GetEventsByTransaction(txID string) []domain.WebhookEvent {
	n.mu.RLock()
	defer n.mu.RUnlock()
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
