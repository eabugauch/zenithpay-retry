.PHONY: build run test clean seed demo

BINARY=zenithpay-retry
PORT?=8080

build:
	go build -o bin/$(BINARY) ./cmd/server

run: build
	PORT=$(PORT) ./bin/$(BINARY)

test:
	go test -v -race ./...

clean:
	rm -rf bin/

# Seed 200 test transactions and process all retries
seed:
	curl -s -X POST http://localhost:$(PORT)/api/seed | python3 -m json.tool

# Full demo: seed data, show analytics, show sample transaction
demo: seed
	@echo "\n=== Recovery Overview ==="
	@curl -s http://localhost:$(PORT)/api/analytics/overview | python3 -m json.tool
	@echo "\n=== Recovery by Decline Reason ==="
	@curl -s http://localhost:$(PORT)/api/analytics/by-decline | python3 -m json.tool
	@echo "\n=== Success Rate by Attempt Number ==="
	@curl -s http://localhost:$(PORT)/api/analytics/by-attempt | python3 -m json.tool
