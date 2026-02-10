package retry

import (
	"context"
	"log/slog"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/store"
)

// Scheduler runs a background loop that checks for due retry attempts and executes them.
type Scheduler struct {
	engine   *Engine
	store    *store.Store
	interval time.Duration
	logger   *slog.Logger
}

// NewScheduler creates a background retry scheduler.
func NewScheduler(engine *Engine, s *store.Store, interval time.Duration, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		engine:   engine,
		store:    s,
		interval: interval,
		logger:   logger,
	}
}

// Start begins the background scheduling loop. It checks for due retries at the configured interval.
func (s *Scheduler) Start(ctx context.Context) {
	s.logger.Info("retry scheduler started", "interval", s.interval)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("retry scheduler stopped")
			return
		case <-ticker.C:
			s.processDueRetries()
		}
	}
}

func (s *Scheduler) processDueRetries() {
	due := s.store.GetDueRetries(time.Now().UTC())

	for _, tx := range due {
		s.logger.Info("scheduler executing due retry",
			"transaction_id", tx.ID,
			"scheduled_for", tx.NextRetryAt,
		)

		if err := s.engine.ExecuteRetry(tx.ID); err != nil {
			s.logger.Error("scheduler retry failed",
				"transaction_id", tx.ID,
				"error", err,
			)
		}
	}
}
