package webhook

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testTransaction(id string, webhookURL string) *domain.Transaction {
	return &domain.Transaction{
		ID:         id,
		Status:     domain.StatusScheduled,
		WebhookURL: webhookURL,
	}
}

func TestNotifier_SendRecordsEvent(t *testing.T) {
	n := NewNotifier(testLogger())
	tx := testTransaction("txn_001", "")

	n.Send(tx, domain.EventRetryScheduled, 0)

	events := n.GetEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].EventType != domain.EventRetryScheduled {
		t.Errorf("expected %s, got %s", domain.EventRetryScheduled, events[0].EventType)
	}
	if events[0].TransactionID != "txn_001" {
		t.Errorf("expected txn_001, got %s", events[0].TransactionID)
	}
	if events[0].Status != domain.StatusScheduled {
		t.Errorf("expected scheduled, got %s", events[0].Status)
	}
}

func TestNotifier_SendMultipleEvents(t *testing.T) {
	n := NewNotifier(testLogger())
	tx := testTransaction("txn_001", "")

	n.Send(tx, domain.EventRetryScheduled, 0)
	n.Send(tx, domain.EventRetryFailed, 1)
	n.Send(tx, domain.EventRetrySucceeded, 2)

	events := n.GetEvents()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestNotifier_GetEventsByTransaction(t *testing.T) {
	n := NewNotifier(testLogger())

	n.Send(testTransaction("txn_001", ""), domain.EventRetryScheduled, 0)
	n.Send(testTransaction("txn_002", ""), domain.EventRetryScheduled, 0)
	n.Send(testTransaction("txn_001", ""), domain.EventRetryFailed, 1)

	events := n.GetEventsByTransaction("txn_001")
	if len(events) != 2 {
		t.Errorf("expected 2 events for txn_001, got %d", len(events))
	}

	events = n.GetEventsByTransaction("txn_002")
	if len(events) != 1 {
		t.Errorf("expected 1 event for txn_002, got %d", len(events))
	}

	events = n.GetEventsByTransaction("nonexistent")
	if len(events) != 0 {
		t.Errorf("expected 0 events for nonexistent, got %d", len(events))
	}
}

func TestNotifier_GetEventsReturnsCopy(t *testing.T) {
	n := NewNotifier(testLogger())
	n.Send(testTransaction("txn_001", ""), domain.EventRetryScheduled, 0)

	events := n.GetEvents()
	events[0].EventType = "mutated"

	original := n.GetEvents()
	if original[0].EventType == "mutated" {
		t.Error("GetEvents should return a copy, not internal slice")
	}
}

func TestNotifier_Clear(t *testing.T) {
	n := NewNotifier(testLogger())
	n.Send(testTransaction("txn_001", ""), domain.EventRetryScheduled, 0)
	n.Send(testTransaction("txn_002", ""), domain.EventRetryFailed, 1)

	n.Clear()

	events := n.GetEvents()
	if len(events) != 0 {
		t.Errorf("expected 0 events after clear, got %d", len(events))
	}
}

func TestNotifier_WebhookHTTPDelivery(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json content type")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := NewNotifier(testLogger())
	tx := testTransaction("txn_webhook", server.URL)

	n.Send(tx, domain.EventRetryScheduled, 0)

	// Wait for async delivery
	time.Sleep(200 * time.Millisecond)

	if received.Load() != 1 {
		t.Errorf("expected 1 webhook delivery, got %d", received.Load())
	}
}

func TestNotifier_WebhookDeliveryFailure(t *testing.T) {
	// Unreachable URL — should not panic
	n := NewNotifier(testLogger())
	tx := testTransaction("txn_fail", "http://127.0.0.1:1") // port 1 is unreachable

	n.Send(tx, domain.EventRetryFailed, 1)

	// Wait for async delivery attempt
	time.Sleep(200 * time.Millisecond)

	// Event should still be recorded even if delivery fails
	events := n.GetEvents()
	if len(events) != 1 {
		t.Errorf("event should be recorded regardless of delivery failure, got %d", len(events))
	}
}

func TestNotifier_NoDeliveryWithoutURL(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n := NewNotifier(testLogger())
	tx := testTransaction("txn_no_url", "") // no webhook URL

	n.Send(tx, domain.EventRetryScheduled, 0)

	time.Sleep(200 * time.Millisecond)

	if received.Load() != 0 {
		t.Error("should not deliver webhook when URL is empty")
	}
}

func TestNotifier_ConcurrentSendAndGet(t *testing.T) {
	n := NewNotifier(testLogger())
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			n.Send(testTransaction("txn_concurrent", ""), domain.EventRetryScheduled, 0)
		}()
		go func() {
			defer wg.Done()
			n.GetEvents()
			n.GetEventsByTransaction("txn_concurrent")
		}()
	}

	wg.Wait()

	events := n.GetEvents()
	if len(events) != 50 {
		t.Errorf("expected 50 events, got %d", len(events))
	}
}
