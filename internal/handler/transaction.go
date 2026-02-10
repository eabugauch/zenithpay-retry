package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/retry"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

// TransactionHandler handles HTTP requests for transaction operations.
type TransactionHandler struct {
	engine   *retry.Engine
	store    *store.Store
	notifier *webhook.Notifier
	logger   *slog.Logger
}

// NewTransactionHandler creates a new transaction handler.
func NewTransactionHandler(engine *retry.Engine, s *store.Store, n *webhook.Notifier, logger *slog.Logger) *TransactionHandler {
	return &TransactionHandler{
		engine:   engine,
		store:    s,
		notifier: n,
		logger:   logger,
	}
}

// Submit handles POST /api/transactions - submit a failed transaction for retry evaluation.
func (h *TransactionHandler) Submit(w http.ResponseWriter, r *http.Request) {
	var req domain.SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.TransactionID == "" {
		writeError(w, http.StatusBadRequest, "transaction_id is required")
		return
	}
	if req.DeclineCode == "" {
		writeError(w, http.StatusBadRequest, "decline_code is required")
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	if req.Currency == "" {
		writeError(w, http.StatusBadRequest, "currency is required")
		return
	}

	resp, err := h.engine.Submit(req)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, resp)
}

// Get handles GET /api/transactions/{id} - get transaction status and retry history.
func (h *TransactionHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "transaction id is required")
		return
	}

	tx, err := h.store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	response := map[string]any{
		"transaction":    tx,
		"webhook_events": h.notifier.GetEventsByTransaction(tx.ID),
	}
	writeJSON(w, http.StatusOK, response)
}

// List handles GET /api/transactions - list all transactions with optional status filter.
func (h *TransactionHandler) List(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	transactions := h.store.List(status)

	response := map[string]any{
		"total":        len(transactions),
		"transactions": transactions,
	}
	writeJSON(w, http.StatusOK, response)
}

// Retry handles POST /api/transactions/{id}/retry - manually trigger next retry.
func (h *TransactionHandler) Retry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "transaction id is required")
		return
	}

	if err := h.engine.ExecuteRetry(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, _ := h.store.Get(id)
	writeJSON(w, http.StatusOK, tx)
}

// ProcessAll handles POST /api/retry/process-all - process all pending retries (demo mode).
func (h *TransactionHandler) ProcessAll(w http.ResponseWriter, r *http.Request) {
	processed, recovered := h.engine.ProcessAllPending()

	response := map[string]any{
		"message":             "All pending retries processed",
		"total_attempts_made": processed,
		"transactions_recovered": recovered,
	}
	writeJSON(w, http.StatusOK, response)
}

// GetWebhookEvents handles GET /api/webhooks/events - list all webhook events.
func (h *TransactionHandler) GetWebhookEvents(w http.ResponseWriter, r *http.Request) {
	events := h.notifier.GetEvents()
	response := map[string]any{
		"total":  len(events),
		"events": events,
	}
	writeJSON(w, http.StatusOK, response)
}

// GetDeclineCodes handles GET /api/decline-codes - list all known decline codes.
func (h *TransactionHandler) GetDeclineCodes(w http.ResponseWriter, r *http.Request) {
	codes := domain.GetAllDeclineCodes()
	strategies := map[string]any{}

	for _, code := range codes[domain.SoftDecline] {
		strategy := domain.GetRetryStrategy(code)
		if strategy != nil {
			delays := make([]string, len(strategy.Delays))
			for i, d := range strategy.Delays {
				delays[i] = d.String()
			}
			strategies[code] = map[string]any{
				"max_attempts":      strategy.MaxAttempts,
				"delays":            delays,
				"use_alt_processor": strategy.UseAltProcessor,
				"description":       strategy.Description,
			}
		}
	}

	response := map[string]any{
		"hard_declines":    codes[domain.HardDecline],
		"soft_declines":    codes[domain.SoftDecline],
		"retry_strategies": strategies,
	}
	writeJSON(w, http.StatusOK, response)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
