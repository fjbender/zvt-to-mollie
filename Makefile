.PHONY: build test run lint

build:
	go build ./...

test:
	go test ./...

run:
	go run ./cmd/zvt-to-mollie

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"
