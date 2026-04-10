BIN := stor
CMD := ./cmd/stor

.PHONY: all build test lint vet fmt check clean

all: check build

build:
	go build -o $(BIN) $(CMD)

test:
	go test -race -cover ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	gofumpt -w .

# check runs all quality gates
check: fmt vet lint test

clean:
	rm -f $(BIN)
	go clean -testcache
