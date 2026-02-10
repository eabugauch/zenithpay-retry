package retry

import (
	"sync"
	"testing"
)

func TestSimulator_HardDecline(t *testing.T) {
	sim := NewSimulator(42)
	result := sim.ProcessPayment("stolen_card", 1, "stripe_latam")

	if result.Success {
		t.Error("hard decline should never succeed")
	}
	if result.ResponseCode != "HARD_DECLINE" {
		t.Errorf("expected HARD_DECLINE, got %s", result.ResponseCode)
	}
	if result.ResponseMessage != "Transaction not retryable" {
		t.Errorf("unexpected message: %s", result.ResponseMessage)
	}
}

func TestSimulator_UnknownDecline(t *testing.T) {
	sim := NewSimulator(42)
	result := sim.ProcessPayment("unknown_code", 1, "stripe_latam")

	if result.Success {
		t.Error("unknown decline should not succeed")
	}
	if result.ResponseCode != "HARD_DECLINE" {
		t.Errorf("expected HARD_DECLINE for unknown code, got %s", result.ResponseCode)
	}
}

func TestSimulator_Deterministic(t *testing.T) {
	// Two simulators with the same seed should produce identical results
	sim1 := NewSimulator(99)
	sim2 := NewSimulator(99)

	codes := []string{"insufficient_funds", "issuer_timeout", "processor_error", "do_not_honor"}
	for _, code := range codes {
		for attempt := 1; attempt <= 3; attempt++ {
			r1 := sim1.ProcessPayment(code, attempt, "stripe_latam")
			r2 := sim2.ProcessPayment(code, attempt, "stripe_latam")
			if r1.Success != r2.Success {
				t.Errorf("non-deterministic for %s attempt %d: %v vs %v", code, attempt, r1.Success, r2.Success)
			}
			if r1.ResponseCode != r2.ResponseCode {
				t.Errorf("non-deterministic response code for %s attempt %d", code, attempt)
			}
		}
	}
}

func TestSimulator_SuccessResponse(t *testing.T) {
	// Use a seed that produces a success for issuer_timeout attempt 1 (40% rate)
	// Try multiple seeds to find one that succeeds
	for seed := int64(0); seed < 100; seed++ {
		sim := NewSimulator(seed)
		result := sim.ProcessPayment("issuer_timeout", 1, "adyen_apac")
		if result.Success {
			if result.ResponseCode != "APPROVED" {
				t.Errorf("expected APPROVED, got %s", result.ResponseCode)
			}
			if result.ResponseMessage == "" {
				t.Error("expected non-empty success message")
			}
			return
		}
	}
	t.Fatal("could not find a seed that produces a success in 100 tries")
}

func TestSimulator_FailureResponse(t *testing.T) {
	// Use a seed that produces a failure for authentication_failed (15% rate)
	for seed := int64(0); seed < 100; seed++ {
		sim := NewSimulator(seed)
		result := sim.ProcessPayment("authentication_failed", 1, "dlocal_br")
		if !result.Success {
			expected := "DECLINE_authentication_failed"
			if result.ResponseCode != expected {
				t.Errorf("expected %s, got %s", expected, result.ResponseCode)
			}
			if result.ResponseMessage == "" {
				t.Error("expected non-empty failure message")
			}
			return
		}
	}
	t.Fatal("could not find a seed that produces a failure in 100 tries")
}

func TestSimulator_AttemptBeyondMaxClamps(t *testing.T) {
	// authentication_failed has 2 PerAttemptRates: [0.15, 0.12]
	// Attempt 5 should clamp to index 1 (last rate)
	sim1 := NewSimulator(42)
	sim2 := NewSimulator(42)

	// Consume the same random values as attempt 5 would
	// by using attempt 2 (index 1) which is the clamped value
	r1 := sim1.ProcessPayment("authentication_failed", 5, "stripe_latam")
	r2 := sim2.ProcessPayment("authentication_failed", 2, "stripe_latam")

	// Both should use the same rate (index 1 = 0.12), so same random value -> same outcome
	if r1.Success != r2.Success {
		t.Errorf("attempt clamping failed: attempt 5 got %v, attempt 2 got %v", r1.Success, r2.Success)
	}
}

func TestSimulator_ConcurrentSafe(t *testing.T) {
	sim := NewSimulator(42)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sim.ProcessPayment("insufficient_funds", 1, "stripe_latam")
			sim.ProcessPayment("issuer_timeout", 2, "adyen_apac")
			sim.ProcessPayment("processor_error", 3, "dlocal_br")
		}()
	}

	wg.Wait()
	// If we get here without panic or race detector complaint, concurrent access is safe
}
