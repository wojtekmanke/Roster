DATABASE_URL ?= postgres://roster:roster@localhost:54329/roster?sslmode=disable
export ROSTER_TEST_DATABASE_URL ?= $(DATABASE_URL)

.PHONY: help
help: ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

.PHONY: db
db: ## Start Postgres in Docker
	docker compose up -d postgres

.PHONY: build
build: ## Build the roster binary
	go build -o bin/roster ./cmd/roster

.PHONY: test
test: ## Run Go tests (needs Postgres, see `make db`)
	go test ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: generate
generate: ## Regenerate GraphQL server code
	cd internal/graph && go tool gqlgen generate
	cd internal/shop/graph && go tool gqlgen generate

.PHONY: web
web: ## Build the dashboard
	cd web && npm ci && npm run build

.PHONY: web-test
web-test: ## Type-check and test the dashboard
	cd web && npm run typecheck && npm test

.PHONY: up
up: ## Run Postgres and Roster with Docker Compose
	docker compose up --build
