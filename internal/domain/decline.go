package domain

import "time"

// RetryStrategy defines how a specific decline code should be retried.
type RetryStrategy struct {
	DeclineCode        string
	Category           DeclineCategory
	MaxAttempts        int
	Delays             []time.Duration
	PerAttemptRates    []float64 // success probability per attempt (for simulation)
	UseAltProcessor    bool      // bonus: try alternative processor on retry
	Description        string
}

// hardDeclineCodes are decline codes that must never be retried.
var hardDeclineCodes = map[string]string{
	"stolen_card":      "Card has been reported as stolen",
	"fraud_suspected":  "Issuer suspects fraudulent activity",
	"invalid_card":     "Card number does not exist",
	"expired_card":     "Card is past its expiration date",
}

// retryStrategies maps soft decline codes to their optimal retry strategy.
// Delays and success rates are calibrated to match observed recovery data:
//   - insufficient_funds: 42% cumulative recovery
//   - issuer_timeout: 68% cumulative recovery
//   - do_not_honor: 31% cumulative recovery
//   - processor_error: ~60% cumulative recovery
//   - authentication_failed: ~25% cumulative recovery
var retryStrategies = map[string]RetryStrategy{
	"insufficient_funds": {
		DeclineCode:     "insufficient_funds",
		Category:        SoftDecline,
		MaxAttempts:     3,
		Delays:          []time.Duration{2 * time.Hour, 24 * time.Hour, 48 * time.Hour},
		PerAttemptRates: []float64{0.12, 0.17, 0.22},
		UseAltProcessor: false,
		Description:     "Customer may add funds; retry with increasing delays",
	},
	"issuer_timeout": {
		DeclineCode:     "issuer_timeout",
		Category:        SoftDecline,
		MaxAttempts:     3,
		Delays:          []time.Duration{0, 5 * time.Minute, 30 * time.Minute},
		PerAttemptRates: []float64{0.40, 0.30, 0.25},
		UseAltProcessor: true,
		Description:     "Network issue; retry immediately via alternative processor",
	},
	"do_not_honor": {
		DeclineCode:     "do_not_honor",
		Category:        SoftDecline,
		MaxAttempts:     3,
		Delays:          []time.Duration{24 * time.Hour, 48 * time.Hour, 72 * time.Hour},
		PerAttemptRates: []float64{0.12, 0.15, 0.10},
		UseAltProcessor: false,
		Description:     "Generic decline with temporary risk flags; retry after cool-down",
	},
	"processor_error": {
		DeclineCode:     "processor_error",
		Category:        SoftDecline,
		MaxAttempts:     3,
		Delays:          []time.Duration{0, 5 * time.Minute, 1 * time.Hour},
		PerAttemptRates: []float64{0.35, 0.25, 0.20},
		UseAltProcessor: true,
		Description:     "Technical failure on processor side; retry via alternative processor",
	},
	"authentication_failed": {
		DeclineCode:     "authentication_failed",
		Category:        SoftDecline,
		MaxAttempts:     2,
		Delays:          []time.Duration{1 * time.Hour, 6 * time.Hour},
		PerAttemptRates: []float64{0.15, 0.12},
		UseAltProcessor: false,
		Description:     "3DS verification incomplete; retry with fresh auth window",
	},
}

// availableProcessors lists the simulated payment processors for multi-processor failover.
var availableProcessors = []string{
	"stripe_latam",
	"adyen_apac",
	"dlocal_br",
	"payu_mx",
	"mercadopago_co",
}

// ClassifyDecline determines whether a decline code is hard or soft.
func ClassifyDecline(code string) (DeclineCategory, string) {
	if reason, ok := hardDeclineCodes[code]; ok {
		return HardDecline, reason
	}
	if strategy, ok := retryStrategies[code]; ok {
		return SoftDecline, strategy.Description
	}
	return HardDecline, "Unknown decline code, treating as hard decline for safety"
}

// GetRetryStrategy returns the retry strategy for a given decline code.
// Returns nil for hard declines.
func GetRetryStrategy(code string) *RetryStrategy {
	if s, ok := retryStrategies[code]; ok {
		return &s
	}
	return nil
}

// GetAvailableProcessors returns processors available for retry, excluding the original.
func GetAvailableProcessors(excludeProcessor string) []string {
	var processors []string
	for _, p := range availableProcessors {
		if p != excludeProcessor {
			processors = append(processors, p)
		}
	}
	return processors
}

// BuildRetryPlan creates a RetryPlan for a soft-declined transaction.
func BuildRetryPlan(declineCode string, originalProcessor string, baseTime time.Time) *RetryPlan {
	strategy := GetRetryStrategy(declineCode)
	if strategy == nil {
		return nil
	}

	scheduledTimes := make([]time.Time, strategy.MaxAttempts)
	for i, delay := range strategy.Delays {
		scheduledTimes[i] = baseTime.Add(delay)
	}

	processors := make([]string, strategy.MaxAttempts)
	altProcessors := GetAvailableProcessors(originalProcessor)
	for i := 0; i < strategy.MaxAttempts; i++ {
		if strategy.UseAltProcessor && i > 0 && len(altProcessors) > 0 {
			processors[i] = altProcessors[(i-1)%len(altProcessors)]
		} else {
			processors[i] = originalProcessor
		}
	}

	return &RetryPlan{
		MaxAttempts:    strategy.MaxAttempts,
		Strategy:       strategy.Description,
		DeclineCode:    declineCode,
		ScheduledTimes: scheduledTimes,
		Processors:     processors,
	}
}

// IsHardDecline checks if a decline code is a hard decline.
func IsHardDecline(code string) bool {
	_, ok := hardDeclineCodes[code]
	return ok
}

// GetAllDeclineCodes returns all known decline codes grouped by category.
func GetAllDeclineCodes() map[DeclineCategory][]string {
	result := map[DeclineCategory][]string{
		HardDecline: {},
		SoftDecline: {},
	}
	for code := range hardDeclineCodes {
		result[HardDecline] = append(result[HardDecline], code)
	}
	for code := range retryStrategies {
		result[SoftDecline] = append(result[SoftDecline], code)
	}
	return result
}
