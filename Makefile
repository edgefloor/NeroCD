GOCACHE_DIR := $(CURDIR)/.cache/go-build
BIN_DIR := $(CURDIR)/bin
WEB_APP_DIR := $(CURDIR)/web/app
WEB_DIST_DIR := $(CURDIR)/web/dist
TOOLS_BIN := $(CURDIR)/.tools/bin
GOLANGCI_LINT_VERSION := v2.10.1
GOLANGCI_LINT := $(TOOLS_BIN)/$(GOLANGCI_LINT_VERSION)/golangci-lint
LINT_BASE_REV ?= HEAD
SQLC_TOOL_IMAGE := golang:1.25.7-bookworm@sha256:564e366a28ad1d70f460a2b97d1d299a562f08707eb0ecb24b659e5bd6c108e1
PRODUCT_PACKAGES := ./cmd/... ./internal/...
TEST_PACKAGES := $(shell go list ./... | grep -v '/agent/skills/')

.PHONY: build test fmt-check vet race lint lint-new lint-install shell-check verify postgres-test generate check-generated web-install web-test web-build web-policy browser-smoke docker-build production-compose-policy-gate local-image-registry-gate identity-artifact-gate runtime-fencing-gate runtime-spool-gate runtime-enrollment-gate runtime-web-enrollment-gate runtime-provenance-gate runtime-compose-gate runtime-web-operator-gate production-profile-gate backup-restore-gate backup-scheduler-gate dogfood-gate system-operations-gate observability-gate release-evidence-accepted-gates release-evidence-gate release-evidence-synthetic-gate ci-release-policy-gate supply-chain release-artifacts contract check clean run smoke docker-up docker-down docker-logs

build: web-build
	mkdir -p "$(BIN_DIR)"
	GOCACHE="$(GOCACHE_DIR)" go build -trimpath -o "$(BIN_DIR)/nerocd" ./cmd/nerocd

test:
	GOCACHE="$(GOCACHE_DIR)" go test $(TEST_PACKAGES)

fmt-check:
	scripts/verify-go.sh

vet:
	GOCACHE="$(GOCACHE_DIR)" go vet $(PRODUCT_PACKAGES)

race:
	GOCACHE="$(GOCACHE_DIR)" go test -race $(PRODUCT_PACKAGES)

lint-install: $(GOLANGCI_LINT)

$(GOLANGCI_LINT):
	@mkdir -p "$(dir $(GOLANGCI_LINT))"
	GOBIN="$(dir $(GOLANGCI_LINT))" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) config verify --config .golangci.yml
	$(GOLANGCI_LINT) run $(PRODUCT_PACKAGES)

# Keep the changed-code lint target available for fast local feedback.
lint-new: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) config verify --config .golangci.yml
	$(GOLANGCI_LINT) run --new-from-rev=$(LINT_BASE_REV) $(PRODUCT_PACKAGES)

shell-check:
	scripts/verify-shell.sh

verify: fmt-check vet test lint shell-check

postgres-test:
	test -n "$$NEROCD_TEST_DATABASE_URL"
	GOCACHE="$(GOCACHE_DIR)" go test ./internal/store

production-profile-gate:
	bash scripts/production-profile-gate-test.sh
	bash scripts/production-profile-gate.sh

backup-restore-gate:
	bash scripts/backup-restore-gate.sh

backup-scheduler-gate:
	bash scripts/backup-scheduler-gate.sh

dogfood-gate:
	bash scripts/dogfood-gate.sh --mode="$${DOGFOOD_MODE:-local}"

# The backup gate owns a real production-configured server and now includes
# the System Operations browser lifecycle before, during, and after backup.
system-operations-gate: backup-restore-gate

observability-gate:
	bash scripts/observability-gate.sh

generate:
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$(CURDIR):/src" -w /src -e GOTOOLCHAIN=local -e GOCACHE=/tmp/go-cache -e GOMODCACHE=/tmp/go-mod "$(SQLC_TOOL_IMAGE)" sh -ec 'go tool sqlc generate'
	cd web/app && bun run api:generate
	cd web/app && bun run routes:generate

check-generated:
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; \
	mkdir -p "$$tmp/db" "$$tmp/internal/store"; \
	cp go.mod go.sum sqlc.yaml "$$tmp/"; \
	cp -R db/migrations "$$tmp/db/"; \
	cp -R internal/store/sql "$$tmp/internal/store/"; \
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$$tmp:/src" -w /src -e GOTOOLCHAIN=local -e GOCACHE=/tmp/go-cache -e GOMODCACHE=/tmp/go-mod "$(SQLC_TOOL_IMAGE)" sh -ec 'go tool sqlc generate'; \
	diff -ru internal/store/sqlcgen "$$tmp/internal/store/sqlcgen"; \
	cd web/app && bun run api:check && bun run routes:check

web-install:
	cd web/app && bun install --frozen-lockfile

web-test: web-install
	cd web/app && bun run test

web-build: web-install
	cd web/app && bun run build

browser-smoke: build
	cd web/app && bun run smoke:browser

docker-build:
	docker build .

production-compose-policy-gate:
	bash scripts/production-compose-policy-gate.sh
	bash scripts/production-compose-policy-gate-test.sh

local-image-registry-gate:
	bash scripts/local-image-registry-gate.sh

identity-artifact-gate:
	bash scripts/identity-artifact-gate.sh

runtime-fencing-gate:
	bash acceptance/runtime-fencing/gate.sh

runtime-spool-gate:
	bash acceptance/runtime-spool/gate.sh

runtime-secrets-gate:
	bash acceptance/runtime-secrets/gate.sh

runtime-enrollment-gate:
	bash acceptance/runtime-enrollment/gate.sh

runtime-web-enrollment-gate: runtime-enrollment-gate

runtime-provenance-gate:
	bash acceptance/runtime-provenance/gate.sh

runtime-compose-gate:
	bash acceptance/runtime-compose/gate.sh

runtime-web-operator-gate:
	bash acceptance/runtime-web-operator/gate.sh

web-policy: web-install
	node scripts/supply-chain-policy.mjs

supply-chain: web-install web-policy

release-artifacts: build web-policy

# These are deliberately explicit: local release evidence never silently
# skips an accepted gate. The evidence script invokes this exact target from a
# clean source checkout (real or the clearly labelled disposable harness).
release-evidence-accepted-gates:
	$(MAKE) test web-test build browser-smoke web-policy contract check-generated docker-build identity-artifact-gate production-profile-gate runtime-fencing-gate runtime-spool-gate runtime-enrollment-gate runtime-provenance-gate runtime-compose-gate runtime-web-operator-gate backup-restore-gate observability-gate

release-evidence-gate:
	bash scripts/release-evidence.sh --real

release-evidence-synthetic-gate:
	bash scripts/release-evidence.sh --synthetic

# Test-only artifact-phase coverage. Unlike release-evidence{-synthetic}-gate,
# this intentionally does not claim accepted-gate evidence.
release-evidence-post-gate-check:
	bash scripts/release-evidence.sh --synthetic-post-gate

# Static workflow checks only. This target intentionally never calls GitHub,
# GHCR, a signer, or any publication endpoint.
ci-release-policy-gate:
	bash scripts/ci-release-policy-gate.sh .
	bash scripts/ci-release-policy-gate-test.sh
	bash scripts/release-artifact-verifier-test.sh

contract:
	GOCACHE="$(GOCACHE_DIR)" go run ./cmd/nerocd contract

check:
	$(MAKE) clean
	$(MAKE) verify web-test build browser-smoke web-policy contract check-generated production-compose-policy-gate local-image-registry-gate docker-build

clean:
	rm -rf -- "$(GOCACHE_DIR)" "$(BIN_DIR)" "$(WEB_APP_DIR)/node_modules" "$(WEB_APP_DIR)/playwright-report" "$(WEB_APP_DIR)/test-results"
	mkdir -p "$(WEB_DIST_DIR)"
	find "$(WEB_DIST_DIR)" -mindepth 1 ! -name .gitkeep -exec rm -rf -- {} +
	touch "$(WEB_DIST_DIR)/.gitkeep"
	rm -f -- artifacts/*.json artifacts/*.txt

run: web-install web-build
	GOCACHE="$(GOCACHE_DIR)" go run ./cmd/nerocd server

smoke: build
	NEROCD_ADDR=$${NEROCD_ADDR:-http://127.0.0.1:8080} ./bin/nerocd smoke

docker-up:
	docker compose up --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f
