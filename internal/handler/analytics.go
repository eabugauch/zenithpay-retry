package handler

import (
	"net/http"
	"sort"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
	"github.com/eabugauch/zenithpay-retry/internal/store"
)

// AnalyticsHandler handles HTTP requests for analytics and reporting.
type AnalyticsHandler struct {
	store *store.Store
}

// NewAnalyticsHandler creates a new analytics handler.
func NewAnalyticsHandler(s *store.Store) *AnalyticsHandler {
	return &AnalyticsHandler{store: s}
}

// Overview handles GET /api/analytics/overview - overall recovery metrics.
func (h *AnalyticsHandler) Overview(w http.ResponseWriter, r *http.Request) {
	all := h.store.GetAll()

	var overview domain.AnalyticsOverview
	overview.TotalTransactions = len(all)

	for _, tx := range all {
		switch tx.DeclineCategory {
		case domain.HardDecline:
			overview.HardDeclines++
		case domain.SoftDecline:
			overview.SoftDeclines++
		}

		switch tx.Status {
		case domain.StatusRecovered:
			overview.Recovered++
		case domain.StatusFailedFinal:
			overview.FailedFinal++
		case domain.StatusScheduled, domain.StatusRetrying:
			overview.PendingRetry++
		}

		overview.TotalRetryAttempts += len(tx.RetryAttempts)
		for _, a := range tx.RetryAttempts {
			if a.Success {
				overview.SuccessfulAttempts++
			}
		}
	}

	if overview.SoftDeclines > 0 {
		overview.RecoveryRate = float64(overview.Recovered) / float64(overview.SoftDeclines) * 100
	}
	if overview.TotalRetryAttempts > 0 {
		overview.EfficiencyRate = float64(overview.SuccessfulAttempts) / float64(overview.TotalRetryAttempts) * 100
	}

	writeJSON(w, http.StatusOK, overview)
}

// ByDeclineReason handles GET /api/analytics/by-decline - recovery rate by decline code.
func (h *AnalyticsHandler) ByDeclineReason(w http.ResponseWriter, r *http.Request) {
	all := h.store.GetAll()

	statsMap := make(map[string]*domain.DeclineReasonStats)
	for _, tx := range all {
		stats, ok := statsMap[tx.DeclineCode]
		if !ok {
			stats = &domain.DeclineReasonStats{
				DeclineCode: tx.DeclineCode,
				Category:    string(tx.DeclineCategory),
			}
			statsMap[tx.DeclineCode] = stats
		}

		stats.Total++
		switch tx.Status {
		case domain.StatusRecovered:
			stats.Recovered++
			for _, a := range tx.RetryAttempts {
				if a.Success {
					stats.AvgAttempts += float64(a.AttemptNumber)
					break
				}
			}
		case domain.StatusFailedFinal:
			stats.Failed++
		case domain.StatusScheduled, domain.StatusRetrying:
			stats.Pending++
		case domain.StatusRejected:
			stats.Failed++
		}
	}

	var softResults, hardResults []domain.DeclineReasonStats
	for _, stats := range statsMap {
		if stats.Recovered > 0 {
			stats.AvgAttempts /= float64(stats.Recovered)
		}
		completed := stats.Recovered + stats.Failed
		if completed > 0 && stats.Category == string(domain.SoftDecline) {
			stats.RecoveryRate = float64(stats.Recovered) / float64(completed) * 100
		}
		if stats.Category == string(domain.SoftDecline) {
			softResults = append(softResults, *stats)
		} else {
			hardResults = append(hardResults, *stats)
		}
	}

	sort.Slice(softResults, func(i, j int) bool {
		return softResults[i].RecoveryRate > softResults[j].RecoveryRate
	})
	sort.Slice(hardResults, func(i, j int) bool {
		return hardResults[i].Total > hardResults[j].Total
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"soft_declines": softResults,
		"hard_declines": hardResults,
	})
}

// ByAttemptNumber handles GET /api/analytics/by-attempt - success rate by attempt number.
func (h *AnalyticsHandler) ByAttemptNumber(w http.ResponseWriter, r *http.Request) {
	all := h.store.GetAll()

	attemptMap := make(map[int]*domain.AttemptStats)
	for _, tx := range all {
		for _, a := range tx.RetryAttempts {
			stats, ok := attemptMap[a.AttemptNumber]
			if !ok {
				stats = &domain.AttemptStats{
					AttemptNumber: a.AttemptNumber,
				}
				attemptMap[a.AttemptNumber] = stats
			}
			stats.TotalAttempts++
			if a.Success {
				stats.Successes++
			}
		}
	}

	result := make([]domain.AttemptStats, 0, len(attemptMap))
	for _, stats := range attemptMap {
		if stats.TotalAttempts > 0 {
			stats.SuccessRate = float64(stats.Successes) / float64(stats.TotalAttempts) * 100
		}
		result = append(result, *stats)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].AttemptNumber < result[j].AttemptNumber
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"by_attempt": result,
	})
}
