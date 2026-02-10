package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/retry"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

func setupTestServer() (*http.ServeMux, *store.Store) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := store.New()
	notifier := webhook.NewNotifier(logger)
	sim := retry.NewSimulator(42)
	engine := retry.NewEngine(s, sim, notifier, logger)

	txHandler := NewTransactionHandler(engine, s, notifier, logger)
	analyticsHandler := NewAnalyticsHandler(s)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/transactions", txHandler.Submit)
	mux.HandleFunc("GET /api/transactions/{id}", txHandler.Get)
	mux.HandleFunc("GET /api/transactions", txHandler.List)
	mux.HandleFunc("POST /api/transactions/{id}/retry", txHandler.Retry)
	mux.HandleFunc("POST /api/retry/process-all", txHandler.ProcessAll)
	mux.HandleFunc("GET /api/analytics/overview", analyticsHandler.Overview)
	mux.HandleFunc("GET /api/analytics/by-decline", analyticsHandler.ByDeclineReason)
	mux.HandleFunc("GET /api/analytics/by-attempt", analyticsHandler.ByAttemptNumber)
	mux.HandleFunc("GET /api/decline-codes", txHandler.GetDeclineCodes)
	mux.HandleFunc("GET /api/webhooks/events", txHandler.GetWebhookEvents)

	return mux, s
}

func postJSON(mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func get(mux http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestSubmitHandler_SoftDecline(t *testing.T) {
	mux, _ := setupTestServer()

	w := postJSON(mux, "/api/transactions", domain.SubmitRequest{
		TransactionID:     "txn_test_001",
		Amount:            299.99,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "insufficient_funds",
	})

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp domain.SubmitResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.RetryEligible {
		t.Error("expected retry eligible")
	}
	if resp.RetryPlan == nil {
		t.Error("expected retry plan")
	}
}

func TestSubmitHandler_HardDecline(t *testing.T) {
	mux, _ := setupTestServer()

	w := postJSON(mux, "/api/transactions", domain.SubmitRequest{
		TransactionID:     "txn_test_hard",
		Amount:            100.00,
		Currency:          "BRL",
		CustomerID:        "cust_002",
		OriginalProcessor: "dlocal_br",
		DeclineCode:       "stolen_card",
	})

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}

	var resp domain.SubmitResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.RetryEligible {
		t.Error("hard decline should not be retry eligible")
	}
	if resp.Status != domain.StatusRejected {
		t.Errorf("expected rejected, got %s", resp.Status)
	}
}

func TestSubmitHandler_MissingFields(t *testing.T) {
	mux, _ := setupTestServer()

	tests := []struct {
		name string
		body domain.SubmitRequest
	}{
		{"missing transaction_id", domain.SubmitRequest{Amount: 100, Currency: "USD", DeclineCode: "stolen_card"}},
		{"missing decline_code", domain.SubmitRequest{TransactionID: "txn_1", Amount: 100, Currency: "USD"}},
		{"zero amount", domain.SubmitRequest{TransactionID: "txn_1", Amount: 0, Currency: "USD", DeclineCode: "stolen_card"}},
		{"missing currency", domain.SubmitRequest{TransactionID: "txn_1", Amount: 100, DeclineCode: "stolen_card"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postJSON(mux, "/api/transactions", tt.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSubmitHandler_Duplicate(t *testing.T) {
	mux, _ := setupTestServer()

	body := domain.SubmitRequest{
		TransactionID:     "txn_dup",
		Amount:            100.00,
		Currency:          "USD",
		CustomerID:        "cust_001",
		OriginalProcessor: "stripe_latam",
		DeclineCode:       "insufficient_funds",
	}

	postJSON(mux, "/api/transactions", body)
	w := postJSON(mux, "/api/transactions", body)
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

func TestGetHandler_Found(t *testing.T) {
	mux, _ := setupTestServer()

	postJSON(mux, "/api/transactions", domain.SubmitRequest{
		TransactionID:     "txn_get_001",
		Amount:            200.00,
		Currency:          "MXN",
		CustomerID:        "cust_003",
		OriginalProcessor: "payu_mx",
		DeclineCode:       "issuer_timeout",
	})

	w := get(mux, "/api/transactions/txn_get_001")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestGetHandler_NotFound(t *testing.T) {
	mux, _ := setupTestServer()
	w := get(mux, "/api/transactions/nonexistent")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestListHandler(t *testing.T) {
	mux, _ := setupTestServer()

	postJSON(mux, "/api/transactions", domain.SubmitRequest{
		TransactionID: "txn_list_1", Amount: 100, Currency: "USD",
		CustomerID: "c1", OriginalProcessor: "stripe_latam", DeclineCode: "insufficient_funds",
	})
	postJSON(mux, "/api/transactions", domain.SubmitRequest{
		TransactionID: "txn_list_2", Amount: 200, Currency: "BRL",
		CustomerID: "c2", OriginalProcessor: "dlocal_br", DeclineCode: "stolen_card",
	})

	w := get(mux, "/api/transactions")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	total := int(resp["total"].(float64))
	if total != 2 {
		t.Errorf("expected 2 transactions, got %d", total)
	}
}

func TestAnalyticsOverview_Empty(t *testing.T) {
	mux, _ := setupTestServer()
	w := get(mux, "/api/analytics/overview")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var overview domain.AnalyticsOverview
	json.NewDecoder(w.Body).Decode(&overview)
	if overview.TotalTransactions != 0 {
		t.Errorf("expected 0 transactions, got %d", overview.TotalTransactions)
	}
}

func TestRetryHandler(t *testing.T) {
	mux, _ := setupTestServer()

	postJSON(mux, "/api/transactions", domain.SubmitRequest{
		TransactionID: "txn_retry_http", Amount: 500, Currency: "USD",
		CustomerID: "c1", OriginalProcessor: "stripe_latam", DeclineCode: "issuer_timeout",
	})

	w := postJSON(mux, "/api/transactions/txn_retry_http/retry", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProcessAllHandler(t *testing.T) {
	mux, _ := setupTestServer()

	postJSON(mux, "/api/transactions", domain.SubmitRequest{
		TransactionID: "txn_process_1", Amount: 100, Currency: "USD",
		CustomerID: "c1", OriginalProcessor: "stripe_latam", DeclineCode: "issuer_timeout",
	})

	w := postJSON(mux, "/api/retry/process-all", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestDeclineCodesHandler(t *testing.T) {
	mux, _ := setupTestServer()
	w := get(mux, "/api/decline-codes")
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}
