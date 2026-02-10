package domain

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadRetryConfig_EmptyPath(t *testing.T) {
	if err := LoadRetryConfig(""); err != nil {
		t.Errorf("empty path should return nil, got %v", err)
	}
}

func TestLoadRetryConfig_NonExistentFile(t *testing.T) {
	err := LoadRetryConfig("/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestLoadRetryConfig_InvalidJSON(t *testing.T) {
	f, _ := os.CreateTemp("", "retry_config_*.json")
	f.WriteString("{invalid json")
	f.Close()
	defer os.Remove(f.Name())

	err := LoadRetryConfig(f.Name())
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLoadRetryConfig_ValidOverride(t *testing.T) {
	// Save original for cleanup
	original := retryStrategies["insufficient_funds"]
	defer func() { retryStrategies["insufficient_funds"] = original }()

	config := `{
		"strategies": {
			"insufficient_funds": {
				"max_attempts": 5,
				"delays": ["1h", "4h", "12h", "24h", "48h"],
				"description": "Custom strategy from config"
			}
		}
	}`

	f, _ := os.CreateTemp("", "retry_config_*.json")
	f.WriteString(config)
	f.Close()
	defer os.Remove(f.Name())

	if err := LoadRetryConfig(f.Name()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	strategy := GetRetryStrategy("insufficient_funds")
	if strategy == nil {
		t.Fatal("expected strategy to exist")
	}
	if strategy.MaxAttempts != 5 {
		t.Errorf("expected 5 attempts, got %d", strategy.MaxAttempts)
	}
	if strategy.Description != "Custom strategy from config" {
		t.Errorf("expected custom description, got %s", strategy.Description)
	}
	if len(strategy.Delays) != 5 {
		t.Errorf("expected 5 delays, got %d", len(strategy.Delays))
	}
}

func TestLoadRetryConfig_InvalidDelay(t *testing.T) {
	original := retryStrategies["insufficient_funds"]
	defer func() { retryStrategies["insufficient_funds"] = original }()

	config := `{
		"strategies": {
			"insufficient_funds": {
				"delays": ["not_a_duration"]
			}
		}
	}`

	f, _ := os.CreateTemp("", "retry_config_*.json")
	f.WriteString(config)
	f.Close()
	defer os.Remove(f.Name())

	err := LoadRetryConfig(f.Name())
	if err == nil {
		t.Error("expected error for invalid delay format")
	}
}

func TestApplyStrategyOverrides_NewDeclineCode(t *testing.T) {
	// Clean up after test
	defer delete(retryStrategies, "custom_decline")

	overrides := map[string]StrategyConfig{
		"custom_decline": {
			MaxAttempts:     2,
			Delays:          []string{"30m", "2h"},
			PerAttemptRates: []float64{0.20, 0.15},
			Description:     "Custom decline code added via config",
		},
	}

	if err := ApplyStrategyOverrides(overrides); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	strategy := GetRetryStrategy("custom_decline")
	if strategy == nil {
		t.Fatal("expected new strategy to be registered")
	}
	if strategy.MaxAttempts != 2 {
		t.Errorf("expected 2 attempts, got %d", strategy.MaxAttempts)
	}
	if strategy.Category != SoftDecline {
		t.Errorf("expected soft decline category, got %s", strategy.Category)
	}
}

func TestApplyStrategyOverrides_ExponentialBackoff(t *testing.T) {
	original := retryStrategies["issuer_timeout"]
	defer func() { retryStrategies["issuer_timeout"] = original }()

	overrides := map[string]StrategyConfig{
		"issuer_timeout": {
			BackoffType:       "exponential",
			BaseDelay:         "5m",
			BackoffMultiplier: 2.0,
			MaxAttempts:       4,
			PerAttemptRates:   []float64{0.40, 0.35, 0.30, 0.25},
		},
	}

	if err := ApplyStrategyOverrides(overrides); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	strategy := GetRetryStrategy("issuer_timeout")
	if strategy.BackoffType != BackoffExponential {
		t.Errorf("expected exponential backoff, got %s", strategy.BackoffType)
	}
	if strategy.BaseDelay != 5*time.Minute {
		t.Errorf("expected 5m base delay, got %v", strategy.BaseDelay)
	}
	if strategy.BackoffMultiplier != 2.0 {
		t.Errorf("expected 2.0 multiplier, got %f", strategy.BackoffMultiplier)
	}
}

func TestApplyStrategyOverrides_BusinessHours(t *testing.T) {
	original := retryStrategies["insufficient_funds"]
	defer func() { retryStrategies["insufficient_funds"] = original }()

	overrides := map[string]StrategyConfig{
		"insufficient_funds": {
			BackoffType:        "business_hours",
			BusinessHoursStart: 9,
			BusinessHoursEnd:   17,
		},
	}

	if err := ApplyStrategyOverrides(overrides); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	strategy := GetRetryStrategy("insufficient_funds")
	if strategy.BackoffType != BackoffBusinessHours {
		t.Errorf("expected business_hours backoff, got %s", strategy.BackoffType)
	}
	if strategy.BusinessHoursStart != 9 {
		t.Errorf("expected start hour 9, got %d", strategy.BusinessHoursStart)
	}
	if strategy.BusinessHoursEnd != 17 {
		t.Errorf("expected end hour 17, got %d", strategy.BusinessHoursEnd)
	}
}

func TestBuildRetryPlan_ExponentialBackoff(t *testing.T) {
	original := retryStrategies["issuer_timeout"]
	defer func() { retryStrategies["issuer_timeout"] = original }()

	retryStrategies["issuer_timeout"] = RetryStrategy{
		DeclineCode:       "issuer_timeout",
		Category:          SoftDecline,
		MaxAttempts:       3,
		PerAttemptRates:   []float64{0.40, 0.30, 0.25},
		UseAltProcessor:   true,
		BackoffType:       BackoffExponential,
		BaseDelay:         10 * time.Minute,
		BackoffMultiplier: 2.0,
		Description:       "Exponential backoff test",
	}

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	plan := BuildRetryPlan("issuer_timeout", "stripe_latam", base)

	if plan == nil {
		t.Fatal("expected plan")
	}

	// Delays: 10m, 20m, 40m (cumulative: 10m, 30m, 70m)
	expected := []time.Duration{10 * time.Minute, 30 * time.Minute, 70 * time.Minute}
	for i, exp := range expected {
		actual := plan.ScheduledTimes[i].Sub(base)
		if actual != exp {
			t.Errorf("attempt %d: expected %v from base, got %v", i+1, exp, actual)
		}
	}
}

func TestBuildRetryPlan_BusinessHours(t *testing.T) {
	original := retryStrategies["insufficient_funds"]
	defer func() { retryStrategies["insufficient_funds"] = original }()

	retryStrategies["insufficient_funds"] = RetryStrategy{
		DeclineCode:        "insufficient_funds",
		Category:           SoftDecline,
		MaxAttempts:        2,
		Delays:             []time.Duration{2 * time.Hour, 24 * time.Hour},
		PerAttemptRates:    []float64{0.12, 0.17},
		BackoffType:        BackoffBusinessHours,
		BusinessHoursStart: 9,
		BusinessHoursEnd:   17,
		Description:        "Business hours test",
	}

	// Base time: 4pm (within business hours) -> +2h = 6pm (outside) -> snaps to next 9am
	base := time.Date(2025, 1, 1, 16, 0, 0, 0, time.UTC)
	plan := BuildRetryPlan("insufficient_funds", "stripe_latam", base)

	if plan == nil {
		t.Fatal("expected plan")
	}

	// First retry: 4pm + 2h = 6pm -> outside business hours -> next day 9am
	first := plan.ScheduledTimes[0]
	if first.Hour() != 9 {
		t.Errorf("expected first retry at 9am, got %d:00", first.Hour())
	}
	if first.Day() != 2 {
		t.Errorf("expected first retry on Jan 2, got Jan %d", first.Day())
	}

	// Second retry: 4pm + 24h = next day 4pm -> within business hours -> no adjustment
	second := plan.ScheduledTimes[1]
	if second.Hour() != 16 {
		t.Errorf("expected second retry at 4pm, got %d:00", second.Hour())
	}
}

func TestBuildRetryPlan_BusinessHours_AlreadyInWindow(t *testing.T) {
	original := retryStrategies["insufficient_funds"]
	defer func() { retryStrategies["insufficient_funds"] = original }()

	retryStrategies["insufficient_funds"] = RetryStrategy{
		DeclineCode:        "insufficient_funds",
		Category:           SoftDecline,
		MaxAttempts:        1,
		Delays:             []time.Duration{2 * time.Hour},
		PerAttemptRates:    []float64{0.12},
		BackoffType:        BackoffBusinessHours,
		BusinessHoursStart: 9,
		BusinessHoursEnd:   17,
		Description:        "Business hours in-window test",
	}

	// Base time: 10am -> +2h = 12pm (within business hours) -> no snap
	base := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	plan := BuildRetryPlan("insufficient_funds", "stripe_latam", base)

	first := plan.ScheduledTimes[0]
	if first.Hour() != 12 {
		t.Errorf("expected retry at 12pm (in business hours), got %d:00", first.Hour())
	}
}

func TestBuildRetryPlan_ExponentialDefaults(t *testing.T) {
	original := retryStrategies["processor_error"]
	defer func() { retryStrategies["processor_error"] = original }()

	retryStrategies["processor_error"] = RetryStrategy{
		DeclineCode:     "processor_error",
		Category:        SoftDecline,
		MaxAttempts:     2,
		PerAttemptRates: []float64{0.35, 0.25},
		BackoffType:     BackoffExponential,
		// BaseDelay and Multiplier left at zero -> should use defaults (5m, 2.0)
		Description: "Exponential defaults test",
	}

	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	plan := BuildRetryPlan("processor_error", "stripe_latam", base)

	if plan == nil {
		t.Fatal("expected plan")
	}

	// Defaults: BaseDelay=5m, Multiplier=2.0
	// Delays: 5m, 10m (cumulative: 5m, 15m)
	expected := []time.Duration{5 * time.Minute, 15 * time.Minute}
	for i, exp := range expected {
		actual := plan.ScheduledTimes[i].Sub(base)
		if actual != exp {
			t.Errorf("attempt %d: expected %v, got %v", i+1, exp, actual)
		}
	}
}

func TestApplyStrategyOverrides_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		config  StrategyConfig
		wantErr string
	}{
		{
			name:    "invalid backoff type",
			config:  StrategyConfig{BackoffType: "random"},
			wantErr: "invalid backoff_type",
		},
		{
			name:    "multiplier too low",
			config:  StrategyConfig{BackoffMultiplier: 0.5},
			wantErr: "backoff_multiplier",
		},
		{
			name:    "multiplier exactly 1.0",
			config:  StrategyConfig{BackoffMultiplier: 1.0},
			wantErr: "backoff_multiplier",
		},
		{
			name:    "business hours start >= end",
			config:  StrategyConfig{BusinessHoursStart: 17, BusinessHoursEnd: 9},
			wantErr: "business_hours_start",
		},
		{
			name:    "business hours end out of range",
			config:  StrategyConfig{BusinessHoursStart: 9, BusinessHoursEnd: 25},
			wantErr: "business hours",
		},
		{
			name:    "per_attempt_rate > 1.0",
			config:  StrategyConfig{PerAttemptRates: []float64{0.5, 1.5}},
			wantErr: "per_attempt_rates",
		},
		{
			name:    "per_attempt_rate negative",
			config:  StrategyConfig{PerAttemptRates: []float64{-0.1}},
			wantErr: "per_attempt_rates",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer delete(retryStrategies, "test_validation")

			err := ApplyStrategyOverrides(map[string]StrategyConfig{
				"test_validation": tt.config,
			})
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestApplyStrategyOverrides_ValidConfigs(t *testing.T) {
	// Ensure valid configs still pass after adding validation
	tests := []struct {
		name   string
		config StrategyConfig
	}{
		{
			name:   "valid exponential",
			config: StrategyConfig{BackoffType: "exponential", BackoffMultiplier: 2.0, BaseDelay: "5m", MaxAttempts: 3},
		},
		{
			name:   "valid business hours",
			config: StrategyConfig{BackoffType: "business_hours", BusinessHoursStart: 9, BusinessHoursEnd: 17},
		},
		{
			name:   "valid fixed with rates",
			config: StrategyConfig{BackoffType: "fixed", PerAttemptRates: []float64{0.0, 0.5, 1.0}},
		},
		{
			name:   "valid multiplier > 1",
			config: StrategyConfig{BackoffMultiplier: 1.5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer delete(retryStrategies, "test_valid")

			err := ApplyStrategyOverrides(map[string]StrategyConfig{
				"test_valid": tt.config,
			})
			if err != nil {
				t.Errorf("expected no error for valid config, got: %v", err)
			}
		})
	}
}

func TestSnapToBusinessHours(t *testing.T) {
	tests := []struct {
		name       string
		hour       int
		expectHour int
		expectDay  int
	}{
		{"within hours", 12, 12, 1},
		{"at start", 9, 9, 1},
		{"before start", 7, 9, 1},
		{"at end", 17, 9, 2},
		{"after end", 20, 9, 2},
		{"midnight", 0, 9, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := time.Date(2025, 1, 1, tt.hour, 30, 0, 0, time.UTC)
			result := snapToBusinessHours(input, 9, 17)
			if result.Hour() != tt.expectHour {
				t.Errorf("expected hour %d, got %d", tt.expectHour, result.Hour())
			}
			if result.Day() != tt.expectDay {
				t.Errorf("expected day %d, got %d", tt.expectDay, result.Day())
			}
		})
	}
}
