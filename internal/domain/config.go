package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// BackoffType defines how retry delays are calculated.
type BackoffType string

const (
	BackoffFixed         BackoffType = "fixed"          // Use static delays from Delays field
	BackoffExponential   BackoffType = "exponential"    // BaseDelay * Multiplier^(attempt-1)
	BackoffBusinessHours BackoffType = "business_hours" // Snap retries to business hours window
)

// RetryConfig is the top-level configuration file structure.
type RetryConfig struct {
	Strategies map[string]StrategyConfig `json:"strategies"`
}

// StrategyConfig is the JSON representation of a retry strategy override.
type StrategyConfig struct {
	MaxAttempts        int       `json:"max_attempts"`
	Delays             []string  `json:"delays,omitempty"`             // e.g. ["2h", "24h", "48h"]
	PerAttemptRates    []float64 `json:"per_attempt_rates,omitempty"`
	UseAltProcessor    bool      `json:"use_alt_processor"`
	BackoffType        string    `json:"backoff_type,omitempty"`       // "fixed", "exponential", "business_hours"
	BaseDelay          string    `json:"base_delay,omitempty"`         // for exponential backoff
	BackoffMultiplier  float64   `json:"backoff_multiplier,omitempty"` // for exponential (default 2.0)
	BusinessHoursStart int       `json:"business_hours_start,omitempty"` // hour (0-23) for business-hours mode
	BusinessHoursEnd   int       `json:"business_hours_end,omitempty"`   // hour (0-23) for business-hours mode
	Description        string    `json:"description,omitempty"`
}

// LoadRetryConfig reads a JSON config file and applies strategy overrides.
// Returns nil if the path is empty (use defaults). Returns an error if the file
// exists but cannot be parsed.
func LoadRetryConfig(path string) error {
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading retry config %s: %w", path, err)
	}

	var config RetryConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parsing retry config %s: %w", path, err)
	}

	return ApplyStrategyOverrides(config.Strategies)
}

// ApplyStrategyOverrides merges strategy configurations into the runtime map.
// Only fields with non-zero values override the defaults.
func ApplyStrategyOverrides(overrides map[string]StrategyConfig) error {
	for code, cfg := range overrides {
		existing, ok := retryStrategies[code]
		if !ok {
			// New soft decline code — build from scratch
			existing = RetryStrategy{
				DeclineCode: code,
				Category:    SoftDecline,
			}
		}

		if cfg.MaxAttempts > 0 {
			existing.MaxAttempts = cfg.MaxAttempts
		}
		if len(cfg.Delays) > 0 {
			delays := make([]time.Duration, len(cfg.Delays))
			for i, d := range cfg.Delays {
				parsed, err := time.ParseDuration(d)
				if err != nil {
					return fmt.Errorf("invalid delay %q for %s: %w", d, code, err)
				}
				delays[i] = parsed
			}
			existing.Delays = delays
		}
		if len(cfg.PerAttemptRates) > 0 {
			existing.PerAttemptRates = cfg.PerAttemptRates
		}
		if cfg.UseAltProcessor {
			existing.UseAltProcessor = true
		}
		if cfg.Description != "" {
			existing.Description = cfg.Description
		}

		// Backoff configuration
		if cfg.BackoffType != "" {
			existing.BackoffType = BackoffType(cfg.BackoffType)
		}
		if cfg.BaseDelay != "" {
			parsed, err := time.ParseDuration(cfg.BaseDelay)
			if err != nil {
				return fmt.Errorf("invalid base_delay %q for %s: %w", cfg.BaseDelay, code, err)
			}
			existing.BaseDelay = parsed
		}
		if cfg.BackoffMultiplier > 0 {
			existing.BackoffMultiplier = cfg.BackoffMultiplier
		}
		if cfg.BusinessHoursStart > 0 || cfg.BusinessHoursEnd > 0 {
			existing.BusinessHoursStart = cfg.BusinessHoursStart
			existing.BusinessHoursEnd = cfg.BusinessHoursEnd
		}

		retryStrategies[code] = existing
	}
	return nil
}
