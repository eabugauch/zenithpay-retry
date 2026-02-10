package domain

import (
	"testing"
	"time"
)

func TestClassifyDecline_HardDeclines(t *testing.T) {
	hardCodes := []string{"stolen_card", "fraud_suspected", "invalid_card", "expired_card"}
	for _, code := range hardCodes {
		t.Run(code, func(t *testing.T) {
			category, reason := ClassifyDecline(code)
			if category != HardDecline {
				t.Errorf("expected HardDecline for %s, got %s", code, category)
			}
			if reason == "" {
				t.Error("expected non-empty reason")
			}
		})
	}
}

func TestClassifyDecline_SoftDeclines(t *testing.T) {
	softCodes := []string{"insufficient_funds", "issuer_timeout", "do_not_honor", "processor_error", "authentication_failed"}
	for _, code := range softCodes {
		t.Run(code, func(t *testing.T) {
			category, reason := ClassifyDecline(code)
			if category != SoftDecline {
				t.Errorf("expected SoftDecline for %s, got %s", code, category)
			}
			if reason == "" {
				t.Error("expected non-empty reason")
			}
		})
	}
}

func TestClassifyDecline_UnknownCode(t *testing.T) {
	category, _ := ClassifyDecline("unknown_code")
	if category != HardDecline {
		t.Errorf("expected HardDecline for unknown code, got %s", category)
	}
}

func TestGetRetryStrategy(t *testing.T) {
	tests := []struct {
		code        string
		expectNil   bool
		maxAttempts int
	}{
		{"insufficient_funds", false, 3},
		{"issuer_timeout", false, 3},
		{"do_not_honor", false, 3},
		{"processor_error", false, 3},
		{"authentication_failed", false, 2},
		{"stolen_card", true, 0},
		{"unknown", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			strategy := GetRetryStrategy(tt.code)
			if tt.expectNil {
				if strategy != nil {
					t.Errorf("expected nil strategy for %s", tt.code)
				}
				return
			}
			if strategy == nil {
				t.Fatalf("expected non-nil strategy for %s", tt.code)
			}
			if strategy.MaxAttempts != tt.maxAttempts {
				t.Errorf("expected %d max attempts for %s, got %d", tt.maxAttempts, tt.code, strategy.MaxAttempts)
			}
			if len(strategy.Delays) != strategy.MaxAttempts {
				t.Errorf("delays count (%d) should match max attempts (%d)", len(strategy.Delays), strategy.MaxAttempts)
			}
			if len(strategy.PerAttemptRates) != strategy.MaxAttempts {
				t.Errorf("per-attempt rates count (%d) should match max attempts (%d)", len(strategy.PerAttemptRates), strategy.MaxAttempts)
			}
		})
	}
}

func TestBuildRetryPlan(t *testing.T) {
	baseTime := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)

	t.Run("soft decline produces plan", func(t *testing.T) {
		plan := BuildRetryPlan("insufficient_funds", "stripe_latam", baseTime)
		if plan == nil {
			t.Fatal("expected non-nil plan")
		}
		if plan.MaxAttempts != 3 {
			t.Errorf("expected 3 max attempts, got %d", plan.MaxAttempts)
		}
		if len(plan.ScheduledTimes) != 3 {
			t.Errorf("expected 3 scheduled times, got %d", len(plan.ScheduledTimes))
		}
		// First retry should be 2 hours after base time
		expected := baseTime.Add(2 * time.Hour)
		if !plan.ScheduledTimes[0].Equal(expected) {
			t.Errorf("first retry expected at %v, got %v", expected, plan.ScheduledTimes[0])
		}
	})

	t.Run("hard decline produces no plan", func(t *testing.T) {
		plan := BuildRetryPlan("stolen_card", "stripe_latam", baseTime)
		if plan != nil {
			t.Error("expected nil plan for hard decline")
		}
	})

	t.Run("alt processor used when configured", func(t *testing.T) {
		plan := BuildRetryPlan("issuer_timeout", "stripe_latam", baseTime)
		if plan == nil {
			t.Fatal("expected non-nil plan")
		}
		// First attempt uses original processor
		if plan.Processors[0] != "stripe_latam" {
			t.Errorf("first attempt should use original processor, got %s", plan.Processors[0])
		}
		// Subsequent attempts should use alternative processors
		if plan.Processors[1] == "stripe_latam" {
			t.Error("second attempt should use alternative processor")
		}
	})
}

func TestIsHardDecline(t *testing.T) {
	if !IsHardDecline("stolen_card") {
		t.Error("stolen_card should be hard decline")
	}
	if IsHardDecline("insufficient_funds") {
		t.Error("insufficient_funds should not be hard decline")
	}
}

func TestGetAvailableProcessors(t *testing.T) {
	processors := GetAvailableProcessors("stripe_latam")
	for _, p := range processors {
		if p == "stripe_latam" {
			t.Error("excluded processor should not appear in result")
		}
	}
	if len(processors) == 0 {
		t.Error("expected at least one alternative processor")
	}
}
