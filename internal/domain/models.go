package domain

import "time"

// DeclineCategory classifies whether a decline is retryable.
type DeclineCategory string

const (
	HardDecline DeclineCategory = "hard"
	SoftDecline DeclineCategory = "soft"
)

// TransactionStatus represents the current state of a transaction in the retry lifecycle.
type TransactionStatus string

const (
	StatusScheduled  TransactionStatus = "scheduled"    // Retry plan created, waiting for first attempt
	StatusRetrying   TransactionStatus = "retrying"     // At least one retry attempted, more pending
	StatusRecovered  TransactionStatus = "recovered"    // A retry attempt succeeded
	StatusFailedFinal TransactionStatus = "failed_final" // All retry attempts exhausted, none succeeded
	StatusRejected   TransactionStatus = "rejected"     // Hard decline, will not retry
)

// Webhook event type constants.
const (
	EventRetryScheduled = "retry.scheduled"
	EventRetrySucceeded = "retry.succeeded"
	EventRetryFailed    = "retry.failed"
	EventRetryExhausted = "retry.exhausted"
)

// Transaction represents a failed payment transaction submitted for retry evaluation.
type Transaction struct {
	ID                string            `json:"id"`
	Amount            float64           `json:"amount"`
	Currency          string            `json:"currency"`
	CustomerID        string            `json:"customer_id"`
	MerchantID        string            `json:"merchant_id"`
	OriginalProcessor string            `json:"original_processor"`
	DeclineCode       string            `json:"decline_code"`
	DeclineCategory   DeclineCategory   `json:"decline_category"`
	Status            TransactionStatus `json:"status"`
	RetryPlan         *RetryPlan        `json:"retry_plan,omitempty"`
	RetryAttempts     []RetryAttempt    `json:"retry_attempts"`
	NextRetryAt       *time.Time        `json:"next_retry_at,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	WebhookURL        string            `json:"webhook_url,omitempty"`
}

// RetryPlan describes the scheduled retry strategy for a soft-declined transaction.
type RetryPlan struct {
	MaxAttempts    int           `json:"max_attempts"`
	Strategy       string        `json:"strategy"`
	DeclineCode    string        `json:"decline_code"`
	ScheduledTimes []time.Time   `json:"scheduled_times"`
	Processors     []string      `json:"processors"`
}

// RetryAttempt records the result of a single retry execution.
type RetryAttempt struct {
	AttemptNumber int       `json:"attempt_number"`
	Processor     string    `json:"processor"`
	ScheduledAt   time.Time `json:"scheduled_at"`
	ExecutedAt    time.Time `json:"executed_at"`
	Success       bool      `json:"success"`
	ResponseCode  string    `json:"response_code"`
	ResponseMsg   string    `json:"response_message"`
}

// SubmitRequest is the API request body for submitting a failed transaction.
type SubmitRequest struct {
	TransactionID     string  `json:"transaction_id"`
	Amount            float64 `json:"amount"`
	Currency          string  `json:"currency"`
	CustomerID        string  `json:"customer_id"`
	MerchantID        string  `json:"merchant_id"`
	OriginalProcessor string  `json:"original_processor"`
	DeclineCode       string  `json:"decline_code"`
	Timestamp         string  `json:"timestamp"`
	WebhookURL        string  `json:"webhook_url,omitempty"`
}

// SubmitResponse is the API response after submitting a failed transaction.
type SubmitResponse struct {
	TransactionID   string          `json:"transaction_id"`
	DeclineCategory DeclineCategory `json:"decline_category"`
	Status          TransactionStatus `json:"status"`
	RetryEligible   bool            `json:"retry_eligible"`
	RetryPlan       *RetryPlan      `json:"retry_plan,omitempty"`
	Message         string          `json:"message"`
}

// AnalyticsOverview provides high-level recovery metrics.
type AnalyticsOverview struct {
	TotalTransactions   int     `json:"total_transactions"`
	HardDeclines        int     `json:"hard_declines"`
	SoftDeclines        int     `json:"soft_declines"`
	Recovered           int     `json:"recovered"`
	FailedFinal         int     `json:"failed_final"`
	PendingRetry        int     `json:"pending_retry"`
	RecoveryRate        float64 `json:"recovery_rate_pct"`
	TotalRetryAttempts  int     `json:"total_retry_attempts"`
	SuccessfulAttempts  int     `json:"successful_attempts"`
	EfficiencyRate      float64 `json:"efficiency_rate_pct"`
}

// DeclineReasonStats provides recovery metrics for a specific decline code.
type DeclineReasonStats struct {
	DeclineCode    string  `json:"decline_code"`
	Category       string  `json:"category"`
	Total          int     `json:"total"`
	Recovered      int     `json:"recovered"`
	Failed         int     `json:"failed"`
	Pending        int     `json:"pending"`
	RecoveryRate   float64 `json:"recovery_rate_pct"`
	AvgAttempts    float64 `json:"avg_attempts_to_recover"`
}

// AttemptStats shows success rate by attempt number.
type AttemptStats struct {
	AttemptNumber  int     `json:"attempt_number"`
	TotalAttempts  int     `json:"total_attempts"`
	Successes      int     `json:"successes"`
	SuccessRate    float64 `json:"success_rate_pct"`
}

// WebhookEvent represents a notification sent to the merchant.
type WebhookEvent struct {
	EventType     string            `json:"event_type"`
	TransactionID string            `json:"transaction_id"`
	Status        TransactionStatus `json:"status"`
	AttemptNumber int               `json:"attempt_number,omitempty"`
	Timestamp     time.Time         `json:"timestamp"`
}
