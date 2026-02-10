package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/handler"
	"github.com/eabugauch/zenithpay-retry/internal/retry"
	"github.com/eabugauch/zenithpay-retry/internal/seed"
	"github.com/eabugauch/zenithpay-retry/internal/store"
	"github.com/eabugauch/zenithpay-retry/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Initialize dependencies
	txStore := store.New()
	notifier := webhook.NewNotifier(logger)
	simulator := retry.NewSimulator(time.Now().UnixNano())
	engine := retry.NewEngine(txStore, simulator, notifier, logger)

	// Initialize handlers
	txHandler := handler.NewTransactionHandler(engine, txStore, notifier, logger)
	analyticsHandler := handler.NewAnalyticsHandler(txStore)

	// Setup routes
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "zenithpay-retry-engine"})
	})

	// Transaction endpoints
	mux.HandleFunc("POST /api/transactions", txHandler.Submit)
	mux.HandleFunc("GET /api/transactions/{id}", txHandler.Get)
	mux.HandleFunc("GET /api/transactions", txHandler.List)
	mux.HandleFunc("POST /api/transactions/{id}/retry", txHandler.Retry)

	// Retry control
	mux.HandleFunc("POST /api/retry/process-all", txHandler.ProcessAll)

	// Analytics endpoints
	mux.HandleFunc("GET /api/analytics/overview", analyticsHandler.Overview)
	mux.HandleFunc("GET /api/analytics/by-decline", analyticsHandler.ByDeclineReason)
	mux.HandleFunc("GET /api/analytics/by-attempt", analyticsHandler.ByAttemptNumber)

	// Reference data
	mux.HandleFunc("GET /api/decline-codes", txHandler.GetDeclineCodes)

	// Webhook events
	mux.HandleFunc("GET /api/webhooks/events", txHandler.GetWebhookEvents)

	// Seed endpoint
	mux.HandleFunc("POST /api/seed", func(w http.ResponseWriter, r *http.Request) {
		count := 200
		txStore.Clear()
		notifier.Clear()

		transactions := seed.GenerateTransactions(count, time.Now().UnixNano())
		submitted := 0
		for _, tx := range transactions {
			if _, err := engine.Submit(tx); err != nil {
				logger.Error("seed submit failed", "transaction_id", tx.TransactionID, "error", err)
				continue
			}
			submitted++
		}

		// Process all retries in accelerated mode
		processed, recovered := engine.ProcessAllPending()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"message":                fmt.Sprintf("Seeded %d transactions and processed retries", submitted),
			"total_seeded":           submitted,
			"retry_attempts_made":    processed,
			"transactions_recovered": recovered,
		})
	})

	// Reset endpoint
	mux.HandleFunc("POST /api/reset", func(w http.ResponseWriter, r *http.Request) {
		txStore.Clear()
		notifier.Clear()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"message": "All data cleared"})
	})

	// Wrap with CORS and logging middleware
	wrappedMux := corsMiddleware(loggingMiddleware(logger, mux))

	// Start background scheduler
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler := retry.NewScheduler(engine, txStore, 30*time.Second, logger)
	go scheduler.Start(ctx)

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      wrappedMux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down server...")
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown error", "error", err)
		}
	}()

	logger.Info("ZenithPay Retry Engine starting", "port", port)
	fmt.Printf("\n  ZenithPay Retry Engine\n")
	fmt.Printf("  ──────────────────────\n")
	fmt.Printf("  Server:     http://localhost:%s\n", port)
	fmt.Printf("  Health:     http://localhost:%s/health\n", port)
	fmt.Printf("  API Docs:   See README.md\n\n")
	fmt.Printf("  Quick Start:\n")
	fmt.Printf("    1. POST /api/seed          → Generate 200 test transactions & process retries\n")
	fmt.Printf("    2. GET  /api/analytics/overview → View recovery metrics\n")
	fmt.Printf("    3. GET  /api/transactions   → Browse all transactions\n\n")

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code for logging.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.statusCode,
			"duration", time.Since(start).String(),
		)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// NOTE: Wildcard CORS is acceptable for this demo/challenge service.
		// Production would restrict to specific merchant origins.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
