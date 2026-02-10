package retry

import (
	"fmt"
	"math/rand"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

// SimResult represents the outcome of a simulated payment processor call.
type SimResult struct {
	Success         bool
	ResponseCode    string
	ResponseMessage string
}

// Simulator simulates payment processor API calls with configurable success rates.
type Simulator struct {
	rng *rand.Rand
}

// NewSimulator creates a new payment processor simulator.
func NewSimulator(seed int64) *Simulator {
	return &Simulator{
		rng: rand.New(rand.NewSource(seed)),
	}
}

// ProcessPayment simulates a retry attempt through a payment processor.
// Success probability is based on the decline code and attempt number,
// using calibrated per-attempt rates from observed recovery data.
func (s *Simulator) ProcessPayment(declineCode string, attemptNum int, processor string) SimResult {
	strategy := domain.GetRetryStrategy(declineCode)
	if strategy == nil {
		return SimResult{
			Success:         false,
			ResponseCode:    "HARD_DECLINE",
			ResponseMessage: "Transaction not retryable",
		}
	}

	idx := attemptNum - 1
	if idx >= len(strategy.PerAttemptRates) {
		idx = len(strategy.PerAttemptRates) - 1
	}
	successRate := strategy.PerAttemptRates[idx]

	roll := s.rng.Float64()
	success := roll < successRate

	if success {
		return SimResult{
			Success:         true,
			ResponseCode:    "APPROVED",
			ResponseMessage: fmt.Sprintf("Transaction approved by %s on attempt %d", processor, attemptNum),
		}
	}

	return SimResult{
		Success:         false,
		ResponseCode:    fmt.Sprintf("DECLINE_%s", declineCode),
		ResponseMessage: fmt.Sprintf("Retry attempt %d failed via %s: %s persists", attemptNum, processor, declineCode),
	}
}
