.PHONY: build run test vet lint clean seed demo docker

BINARY=zenithpay-retry
PORT?=8080

build:
	go build -o bin/$(BINARY) ./cmd/server

run: build
	PORT=$(PORT) ./bin/$(BINARY)

test:
	go test -v -race -count=1 ./...

vet:
	go vet ./...

lint: vet
	@echo "go vet passed"

clean:
	rm -rf bin/

docker:
	docker build -t $(BINARY) .

docker-run: docker
	docker run -p $(PORT):8080 $(BINARY)

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
