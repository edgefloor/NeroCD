GOCACHE_DIR := $(CURDIR)/.cache/go-build
BIN_DIR := $(CURDIR)/bin
WEB_APP_DIR := $(CURDIR)/web/app
WEB_DIST_DIR := $(CURDIR)/web/dist
SQLC_TOOL_IMAGE := golang:1.25.7-bookworm@sha256:564e366a28ad1d70f460a2b97d1d299a562f08707eb0ecb24b659e5bd6c108e1

.PHONY: build test postgres-test generate check-generated web-install web-test web-build web-policy browser-smoke docker-build runtime-fencing-gate runtime-spool-gate runtime-secrets-gate runtime-enrollment-gate supply-chain release-artifacts contract check clean run smoke docker-up docker-down docker-logs

build: web-build
	mkdir -p "$(BIN_DIR)"
	GOCACHE="$(GOCACHE_DIR)" go build -trimpath -o "$(BIN_DIR)/nerocd" ./cmd/nerocd

test:
	GOCACHE="$(GOCACHE_DIR)" go test ./...

postgres-test:
	test -n "$$NEROCD_TEST_DATABASE_URL"
	GOCACHE="$(GOCACHE_DIR)" go test ./internal/store

generate:
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$(CURDIR):/src" -w /src -e GOTOOLCHAIN=local -e GOCACHE=/tmp/go-cache -e GOMODCACHE=/tmp/go-mod "$(SQLC_TOOL_IMAGE)" sh -ec 'go tool sqlc generate'

check-generated:
	@set -eu; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT HUP INT TERM; \
	mkdir -p "$$tmp/db" "$$tmp/internal/store"; \
	cp go.mod go.sum sqlc.yaml "$$tmp/"; \
	cp -R db/migrations "$$tmp/db/"; \
	cp -R internal/store/sql "$$tmp/internal/store/"; \
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$$tmp:/src" -w /src -e GOTOOLCHAIN=local -e GOCACHE=/tmp/go-cache -e GOMODCACHE=/tmp/go-mod "$(SQLC_TOOL_IMAGE)" sh -ec 'go tool sqlc generate'; \
	diff -ru internal/store/sqlcgen "$$tmp/internal/store/sqlcgen"

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

runtime-fencing-gate:
	bash acceptance/runtime-fencing/gate.sh

runtime-spool-gate:
	bash acceptance/runtime-spool/gate.sh

runtime-secrets-gate:
	bash acceptance/runtime-secrets/gate.sh

runtime-enrollment-gate:
	bash acceptance/runtime-enrollment/gate.sh

web-policy: web-install
	node scripts/supply-chain-policy.mjs

supply-chain: web-install web-policy

release-artifacts: build web-policy

contract:
	GOCACHE="$(GOCACHE_DIR)" go run ./cmd/nerocd contract

check:
	$(MAKE) clean
	$(MAKE) test web-test build browser-smoke web-policy contract check-generated docker-build

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
