.DEFAULT_GOAL := help
SHELL := /bin/bash

COMPOSE := docker compose -f deploy/docker-compose.yml
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/sabin-bhattarai/ims-backend/internal/api.Version=$(VERSION)
# The coverage gate CI enforces. Raise it as coverage improves; never lower it
# to make a red build green.
COVERAGE_MIN ?= 70

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# --- local development ------------------------------------------------------

.PHONY: dev
dev: ## Start the full local stack (Postgres, Redis, Mailpit, API, worker)
	$(COMPOSE) up --build -d
	@echo "API      http://localhost:8080"
	@echo "Docs     http://localhost:8080/docs/index.html"
	@echo "Mailpit  http://localhost:8025"
	@echo "Queue    http://localhost:8081"

.PHONY: down
down: ## Stop the local stack
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the local stack and delete its volumes
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Tail API and worker logs
	$(COMPOSE) logs -f api worker

.PHONY: run
run: ## Run the API against a local .env
	go run ./cmd/api

.PHONY: run-worker
run-worker: ## Run the background worker
	go run ./cmd/worker

.PHONY: seed
seed: ## Load demo data (idempotent; refuses to run against production)
	go run ./cmd/seed

# --- build ------------------------------------------------------------------

.PHONY: build
build: ## Build the api, worker and seed binaries into ./bin
	@mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/api ./cmd/api
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/worker ./cmd/worker
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/seed ./cmd/seed

.PHONY: docker-build
docker-build: ## Build the production container image
	docker build -f deploy/Dockerfile --build-arg VERSION=$(VERSION) -t ims-backend:$(VERSION) .

# --- quality ----------------------------------------------------------------

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -w .

.PHONY: lint
lint: ## Run golangci-lint (see .golangci.yml)
	@command -v golangci-lint >/dev/null || { \
		echo "golangci-lint not found: brew install golangci-lint"; exit 1; }
	golangci-lint run ./...

.PHONY: vet
vet: ## Run go vet, including integration-tagged files
	go vet ./...
	go vet -tags=integration ./...

.PHONY: test
test: ## Run unit tests
	go test -race ./...

.PHONY: test-integration
test-integration: ## Run integration tests (requires Docker; uses testcontainers)
	go test -tags=integration -race -timeout=15m ./...

.PHONY: cover
cover: ## Run unit+integration tests and enforce the coverage gate on internal/
	go test -tags=integration -race -timeout=15m \
		-coverprofile=coverage.out -coverpkg=./internal/... ./...
	@go tool cover -func=coverage.out | tail -1
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print substr($$3, 1, length($$3)-1)}'); \
	echo "coverage: $$total% (minimum $(COVERAGE_MIN)%)"; \
	if (( $$(echo "$$total < $(COVERAGE_MIN)" | bc -l) )); then \
		echo "FAIL: coverage $$total% is below the $(COVERAGE_MIN)% gate"; exit 1; \
	fi

.PHONY: cover-html
cover-html: ## Open the HTML coverage report
	go tool cover -html=coverage.out

# --- database ---------------------------------------------------------------

.PHONY: migrate-up
migrate-up: ## Apply all pending migrations
	@command -v migrate >/dev/null || { \
		echo "migrate not found: brew install golang-migrate"; exit 1; }
	migrate -path migrations -database "$$DATABASE_URL" up

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration
	migrate -path migrations -database "$$DATABASE_URL" down 1

.PHONY: migrate-new
migrate-new: ## Create a migration pair: make migrate-new NAME=add_widgets
	@test -n "$(NAME)" || { echo "usage: make migrate-new NAME=add_widgets"; exit 1; }
	migrate create -ext sql -dir migrations -seq $(NAME)

# --- API contract -----------------------------------------------------------

.PHONY: docs
docs: ## Regenerate the OpenAPI spec from code annotations
	@command -v swag >/dev/null || { \
		echo "swag not found: go install github.com/swaggo/swag/cmd/swag@latest"; exit 1; }
	swag init \
		--generalInfo cmd/api/main.go \
		--dir ./ \
		--output docs \
		--outputTypes go,json,yaml \
		--parseDependency --parseInternal
	@echo "docs/swagger.yaml regenerated — copy to docs/openapi.yaml and commit both"
	@cp docs/swagger.yaml docs/openapi.yaml

.PHONY: docs-check
docs-check: ## Fail if the committed spec is stale (CI guard)
	@$(MAKE) docs >/dev/null
	@git diff --exit-code -- docs/ || { \
		echo "FAIL: docs/openapi.yaml is out of date. Run 'make docs' and commit the result."; exit 1; }

.PHONY: ci
ci: vet test ## What CI runs on every pull request
