# SERK Construction
GO ?= go
ADMIN_DSN ?= postgres://postgres:dev-only-postgres-password-not-for-production@localhost:5432/serk_construction?sslmode=disable

.PHONY: gen-docs all build test test-unit test-integration vet fmt migrate up seed reset dev-up dev-down check-prod-config clean

all: fmt vet test build

# Containerised local development environment (runlocal.md)
up:
	docker compose -p serk -f deploy/docker-compose.dev.yml up -d --build

seed:
	scripts/dev-seed.sh

reset:
	docker compose -p serk -f deploy/docker-compose.dev.yml --profile tools down -v

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o bin/ ./cmd/serk-api ./cmd/serk-migrate ./cmd/serk-relay

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && false)

vet:
	$(GO) vet ./...

test-unit:
	$(GO) test -race -count=1 ./pkg/... ./internal/config/

# PostgreSQL integration, adversarial and concurrency suites. ADMIN_DSN must
# be a disposable database whose role can CREATE ROLE (tests create and drop
# per-tenant roles).
test-integration:
	SERK_TEST_ADMIN_DSN="$(ADMIN_DSN)" $(GO) test -race -count=1 ./...

test: test-integration

migrate:
	SERK_ADMIN_DSN="$(ADMIN_DSN)" $(GO) run ./cmd/serk-migrate

dev-up:
	docker compose -f deploy/docker-compose.dev.yml up -d

dev-down:
	docker compose -f deploy/docker-compose.dev.yml down -v

# Validate a production environment file without starting the server.
check-prod-config:
	$(GO) run ./cmd/serk-api check-config

clean:
	rm -rf bin

# Regenerate the documents that are generated from the code (a test fails if they are out of date),
# and add stubs to api/openapi.yaml for any new route.
gen-docs:
	python3 tools/gen_openapi.py
	UPDATE_KPI_DOC=1 $(GO) test ./internal/kernel -run 'TestKPICatalogueDocumentIsCurrent|TestReportCatalogueDocumentIsCurrent'
	UPDATE_ROLE_DOC=1 $(GO) test ./pkg/auth -run TestRoleMatrixDocumentIsCurrent
