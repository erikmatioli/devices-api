# Developer entry points. Run `make help` to list them.

# On Windows, make falls back to cmd.exe unless sh.exe is on PATH, and the
# recipes below are POSIX shell. Use the shell bundled with Git for Windows so
# they behave the same from PowerShell, cmd and Git Bash.
ifeq ($(OS),Windows_NT)
  GIT_CORE_DIR := $(shell git --exec-path)
  ifneq ($(findstring /libexec/git-core,$(GIT_CORE_DIR)),)
    SHELL := $(subst /clangarm64/libexec/git-core,/bin/sh.exe,$(subst /mingw64/libexec/git-core,/bin/sh.exe,$(GIT_CORE_DIR)))
  endif
  # Keep container paths such as -w /src from being rewritten to Windows paths.
  export MSYS_NO_PATHCONV := 1
endif

APP      := devices-api
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
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

.PHONY: build
build: ## Compile the API binary into ./bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(APP)$(EXE) ./cmd/api

.PHONY: run
run: ## Run the API locally using settings from .env
	go run ./cmd/api

.PHONY: test
test: ## Run unit tests
	go test $(TEST_FLAGS) ./...

.PHONY: test-integration
test-integration: ## Run unit and integration tests (needs Docker)
	go test $(TEST_FLAGS) -tags=integration -count=1 ./...

.PHONY: test-docker
test-docker: ## Run all tests with the race detector in a Linux container (needs `make up`)
	docker run --rm --network devices-api_default -v "$(CURDIR):/src" -v devices-api-gomod:/go/pkg/mod -w /src \
	  -e TEST_DATABASE_URL="postgres://devices:devices@postgres:5432/devices?sslmode=disable" \
	  golang:1.27 go test -race -tags=integration -count=1 ./...

.PHONY: cover
cover: ## Run all tests with coverage and print the summary (needs Docker)
	go test $(TEST_FLAGS) -tags=integration -count=1 -covermode=atomic -coverprofile=$(COVER) ./...
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

# Token defaults for `make token`.
SUB   ?= local-developer
SCOPE ?= devices:read devices:write devices:delete
TTL   ?= 1h

.PHONY: token
token: ## Print a development access token (SUB, SCOPE, TTL)
	@go run ./cmd/tokengen -sub "$(SUB)" -scope "$(SCOPE)" -ttl $(TTL)

.PHONY: load
load: ## Run the k6 smoke load test against the running stack (VUS, DURATION)
	docker run --rm -i --network devices-api_default -e BASE_URL=http://api:8080 \
	  -e TOKEN="$$(go run ./cmd/tokengen -ttl 10m)" -e VUS=$(or $(VUS),20) -e DURATION=$(or $(DURATION),30s) \
	  grafana/k6:0.55.0 run - < test/load/smoke.js

.PHONY: postman
postman: ## Run the Postman collection with Newman against the running stack
	npx --yes newman@6.2.1 run postman/devices-api.postman_collection.json -e postman/local.postman_environment.json

.PHONY: check-rules
check-rules: ## Check and unit-test the Prometheus recording and alerting rules (needs Docker)
	docker run --rm -v "$(CURDIR)/deploy/prometheus:/rules:ro" -w /rules --entrypoint promtool prom/prometheus:v3.0.1 check rules rules.yml
	docker run --rm -v "$(CURDIR)/deploy/prometheus:/rules:ro" -w /rules --entrypoint promtool prom/prometheus:v3.0.1 test rules rules.test.yml

.PHONY: up
up: ## Build the images (API and tokengen) and start the API and PostgreSQL
	docker compose --profile tools build
	docker compose up -d

.PHONY: observability
observability: ## Start the stack with the collector, Jaeger, Prometheus and Grafana
	docker compose --profile tools build
	OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 docker compose --profile observability up -d

.PHONY: down
down: ## Stop the containers, including optional profiles (keeps the database volume)
	docker compose --profile observability --profile tools down

.PHONY: logs
logs: ## Follow the API container logs
	docker compose logs -f api

.PHONY: docker-build
docker-build: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(APP):$(VERSION) .
