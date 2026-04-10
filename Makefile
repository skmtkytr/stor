BIN := stor
CMD := ./cmd/stor
UI  := ui/frontend

.PHONY: all build test lint vet fmt check clean daemon ui

all: check build

ui:
	cd $(UI) && bun install && bun run build

build: ui
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

# run daemon in foreground
daemon: build
	./$(BIN) daemon

clean:
	rm -f $(BIN)
	rm -rf $(UI)/node_modules $(UI)/.svelte-kit ui/dist
	go clean -testcache
