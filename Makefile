# Developer entry points. Run `make help` to list them.

APP       := devices-api
BIN_DIR   := bin
COVER     := coverage.out
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

# The race detector needs cgo, which is usually unavailable on Windows.
ifeq ($(OS),Windows_NT)
  TEST_FLAGS ?=
  EXE := .exe
else
  TEST_FLAGS ?= -race
  EXE :=
endif

# Local settings for `make run` (see .env.example).
-include .env
export

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

.PHONY: build
build: ## Compile the API binary into ./bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP)$(EXE) ./cmd/api

.PHONY: run
run: ## Run the API locally using settings from .env
	go run ./cmd/api

.PHONY: test
test: ## Run unit tests
	go test $(TEST_FLAGS) ./...

.PHONY: cover
cover: ## Run tests with coverage and print the summary
	go test $(TEST_FLAGS) -covermode=atomic -coverprofile=$(COVER) ./...
	go tool cover -func=$(COVER)

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format the code
	golangci-lint fmt ./...

.PHONY: tidy
tidy: ## Tidy and verify module dependencies
	go mod tidy
	go mod verify

.PHONY: up
up: ## Build and start the API and PostgreSQL containers
	docker compose up --build -d

.PHONY: down
down: ## Stop the containers (keeps the database volume)
	docker compose down

.PHONY: logs
logs: ## Follow the API container logs
	docker compose logs -f api

.PHONY: docker-build
docker-build: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(APP):$(VERSION) .
