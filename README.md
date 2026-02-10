# ZenithPay Retry Engine

An intelligent payment retry orchestration service that recovers failed transactions through smart retry logic, saving merchants from revenue loss due to soft declines.

Built for the **Yuno Engineering Challenge**: solving the "Approval Rate Crisis" for VoltCommerce — a merchant losing $450K/month to recoverable payment failures.

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   Merchant   │────▶│  Retry API   │────▶│  Classifier  │
│  (Submit TX) │     │  (HTTP)      │     │  (Hard/Soft) │
└──────────────┘     └──────┬───────┘     └──────┬───────┘
                            │                     │
                     ┌──────▼───────┐     ┌──────▼───────┐
                     │    Store     │◀───▶│ Retry Engine │
                     │  (In-Memory) │     │  (Scheduler) │
                     └──────┬───────┘     └──────┬───────┘
                            │                     │
                     ┌──────▼───────┐     ┌──────▼───────┐
                     │  Analytics   │     │  Simulator   │
                     │  (Metrics)   │     │ (Processors) │
                     └──────────────┘     └──────┬───────┘
                                                  │
                                          ┌───────▼──────┐
                                          │   Webhook    │
                                          │  (Notify)    │
                                          └──────────────┘
```

**Key design decisions:**
- **Standard library only** — zero external dependencies, uses Go 1.22+ `net/http` routing
- **In-memory store** with `sync.RWMutex` for thread-safe concurrent access
- **Background scheduler** checks for due retries every 30 seconds
- **Deterministic simulation** with per-attempt success probabilities calibrated to match real-world recovery data
- **Clean separation** of concerns: domain models, retry engine, store, handlers, webhook notifier

## Prerequisites

- **Go 1.23+** — uses `net/http` ServeMux routing introduced in Go 1.22
- **Docker** (optional) — for containerized deployment

## Quick Start

```bash
# Build and run
make run

# Or directly
go run ./cmd/server
```

The server starts on `http://localhost:8080`.

### Docker

```bash
docker build -t zenithpay-retry .
docker run -p 8080:8080 zenithpay-retry
```

## Demo Walkthrough

### 1. Seed test data (200 transactions across 7 days)

```bash
curl -X POST http://localhost:8080/api/seed | jq
```

This generates 200 failed transactions (70% soft declines, 30% hard declines) across 3+ currencies and processes all retries in accelerated mode.

### 2. View recovery analytics

```bash
# Overall metrics
curl http://localhost:8080/api/analytics/overview | jq

# Recovery rate by decline reason
curl http://localhost:8080/api/analytics/by-decline | jq

# Success rate by attempt number
curl http://localhost:8080/api/analytics/by-attempt | jq
```

### 3. Submit a single failed transaction

```bash
# Soft decline — will be scheduled for retry
curl -X POST http://localhost:8080/api/transactions \
  -H "Content-Type: application/json" \
  -d '{
    "transaction_id": "txn_demo_001",
    "amount": 299.99,
    "currency": "USD",
    "customer_id": "cust_12345",
    "merchant_id": "voltcommerce",
    "original_processor": "stripe_latam",
    "decline_code": "insufficient_funds",
    "webhook_url": "https://voltcommerce.com/webhooks"
  }' | jq
```

Response shows the retry plan with scheduled times:
```json
{
  "transaction_id": "txn_demo_001",
  "decline_category": "soft",
  "status": "scheduled",
  "retry_eligible": true,
  "retry_plan": {
    "max_attempts": 3,
    "strategy": "Customer may add funds; retry with increasing delays",
    "scheduled_times": ["2h from now", "24h from now", "48h from now"],
    "processors": ["stripe_latam", "stripe_latam", "stripe_latam"]
  }
}
```

### 4. Hard decline — correctly rejected

```bash
curl -X POST http://localhost:8080/api/transactions \
  -H "Content-Type: application/json" \
  -d '{
    "transaction_id": "txn_demo_002",
    "amount": 150.00,
    "currency": "BRL",
    "customer_id": "cust_99999",
    "merchant_id": "voltcommerce",
    "original_processor": "dlocal_br",
    "decline_code": "stolen_card"
  }' | jq
```

Response:
```json
{
  "transaction_id": "txn_demo_002",
  "decline_category": "hard",
  "status": "rejected",
  "retry_eligible": false,
  "message": "Hard decline: Card has been reported as stolen. Transaction will not be retried."
}
```

### 5. Check transaction status and retry history

```bash
curl http://localhost:8080/api/transactions/txn_demo_001 | jq
```

### 6. Manually trigger retry (or wait for the background scheduler)

```bash
curl -X POST http://localhost:8080/api/transactions/txn_demo_001/retry | jq
```

## API Reference

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/health` | Health check |
| `POST` | `/api/transactions` | Submit a failed transaction for retry evaluation |
| `GET` | `/api/transactions/{id}` | Get transaction status and full retry history |
| `GET` | `/api/transactions?status=recovered` | List transactions with optional status filter |
| `POST` | `/api/transactions/{id}/retry` | Manually trigger next retry attempt |
| `POST` | `/api/retry/process-all` | Process all pending retries (accelerated/demo mode) |
| `GET` | `/api/analytics/overview` | Overall recovery metrics (rate, efficiency) |
| `GET` | `/api/analytics/by-decline` | Recovery rate breakdown by decline reason |
| `GET` | `/api/analytics/by-attempt` | Success rate by retry attempt number |
| `GET` | `/api/decline-codes` | List all decline codes and retry strategies |
| `GET` | `/api/webhooks/events` | View all webhook notification events |
| `POST` | `/api/seed` | Generate 200 test transactions and process retries |
| `POST` | `/api/reset` | Clear all data |

## Retry Strategies by Decline Type

| Decline Code | Category | Max Attempts | Delays | Recovery Target | Rationale |
|-------------|----------|-------------|--------|----------------|-----------|
| `insufficient_funds` | Soft | 3 | 2h, 24h, 48h | ~42% | Customer may add funds over time |
| `issuer_timeout` | Soft | 3 | 0s, 5m, 30m | ~68% | Network issue; immediate retry via alt processor |
| `do_not_honor` | Soft | 3 | 24h, 48h, 72h | ~31% | Temporary risk flags; needs cool-down period |
| `processor_error` | Soft | 3 | 0s, 5m, 1h | ~60% | Technical failure; retry via alternative processor |
| `authentication_failed` | Soft | 2 | 1h, 6h | ~25% | 3DS incomplete; fresh auth window needed |
| `stolen_card` | Hard | 0 | — | 0% | Never retry |
| `fraud_suspected` | Hard | 0 | — | 0% | Never retry |
| `invalid_card` | Hard | 0 | — | 0% | Never retry |
| `expired_card` | Hard | 0 | — | 0% | Never retry |

**Why these delays?**
- **Insufficient funds**: Longer delays give customers time to replenish accounts. Payday cycles (24h, 48h) align with when balance becomes available.
- **Issuer timeout**: Network glitches are usually transient. Immediate retry often works; short backoff covers cascading failures.
- **Do not honor**: Generic decline often caused by velocity limits or temporary risk scoring. Long cool-down resets issuer risk flags.
- **Processor error**: Technical failures on the processor side. Immediate retry through an alternative processor bypasses the issue.
- **Authentication failed**: Customer may have closed the 3DS flow accidentally. New window with reasonable delay.

## Advanced Capabilities

### Multi-Processor Failover
For `issuer_timeout` and `processor_error` declines, retry attempts are routed through alternative payment processors. The system maintains a pool of 5 simulated processors (`stripe_latam`, `adyen_apac`, `dlocal_br`, `payu_mx`, `mercadopago_co`) and selects alternatives automatically.

### Smart Scheduling
The background scheduler runs every 30 seconds, checking for due retry attempts. Retry delays are calibrated based on decline type behavior patterns rather than fixed intervals. Per-attempt success probabilities increase with later attempts for some decline types, reflecting real-world patterns.

### Webhook Notifications
The service emits webhook events at every state transition, with HTTP POST delivery to merchant-configured URLs:
- `retry.scheduled` — transaction accepted and retry plan created
- `retry.succeeded` — transaction recovered on a retry attempt
- `retry.failed` — a retry attempt failed (more attempts pending)
- `retry.exhausted` — all retry attempts used, transaction marked as permanently failed

View events at `GET /api/webhooks/events` or per-transaction at `GET /api/transactions/{id}`.

## Test Data

The seed endpoint generates 200 transactions with:
- **70% soft declines** (weighted: 30% insufficient_funds, 25% do_not_honor, 20% issuer_timeout, 15% processor_error, 10% authentication_failed)
- **30% hard declines** (evenly distributed across stolen_card, fraud_suspected, invalid_card, expired_card)
- **5 currencies**: USD, BRL, MXN, COP, PEN
- **5 processors**: stripe_latam, adyen_apac, dlocal_br, payu_mx, mercadopago_co
- **Timestamps** spread across a 7-day window

## Running Tests

```bash
make test
# or
go test -v -race ./...
```

## Project Structure

```
zenithpay-retry/
├── cmd/server/main.go          # Entry point, routing, middleware
├── internal/
│   ├── domain/
│   │   ├── models.go           # Transaction, RetryPlan, analytics types
│   │   ├── decline.go          # Decline classification and retry strategies
│   │   └── decline_test.go     # Domain logic tests (table-driven)
│   ├── store/
│   │   ├── memory.go           # Thread-safe in-memory store with deep copy
│   │   └── memory_test.go      # Store tests incl. concurrency
│   ├── retry/
│   │   ├── engine.go           # Core retry orchestration logic
│   │   ├── engine_test.go      # Engine unit tests
│   │   ├── simulator.go        # Thread-safe payment processor simulation
│   │   └── scheduler.go        # Background retry scheduler
│   ├── handler/
│   │   ├── transaction.go      # Transaction API handlers
│   │   ├── analytics.go        # Analytics API handlers
│   │   └── handler_test.go     # HTTP integration tests
│   ├── seed/
│   │   └── generator.go        # Test data generation (200 transactions)
│   └── webhook/
│       └── notifier.go         # Webhook notification with HTTP delivery
├── .gitignore
├── Dockerfile                  # Multi-stage Docker build
├── Makefile
├── go.mod
└── README.md
```

## Assumptions & Design Decisions

1. **In-memory storage**: Chose simplicity over persistence since this is a prototype. Production would use PostgreSQL with proper transaction isolation.
2. **Simulated processors**: Retry attempts use a probabilistic simulator with per-attempt success rates calibrated to match the scenario's observed recovery data (42% for insufficient_funds, 68% for issuer_timeout, etc.).
3. **Accelerated demo mode**: The `POST /api/seed` and `POST /api/retry/process-all` endpoints process all retries immediately, bypassing scheduled delays for demonstration purposes. The background scheduler handles real-time retries.
4. **Unknown decline codes** are treated as hard declines for safety — never retry what you don't understand.
5. **Idempotency**: The same transaction ID cannot be submitted twice, preventing duplicate retry chains.
