GOCACHE_DIR := $(CURDIR)/.cache/go-build

.PHONY: build test postgres-test bobgen web-install web-test web-build web-policy browser-smoke supply-chain release-artifacts contract check run smoke docker-up docker-down docker-logs

build: web-install web-build
	GOCACHE=$(GOCACHE_DIR) go build -o bin/nerocd ./cmd/nerocd

test:
	GOCACHE=$(GOCACHE_DIR) go test ./...

postgres-test:
	test -n "$$NEROCD_TEST_DATABASE_URL"
	GOCACHE=$(GOCACHE_DIR) go test ./internal/store

bobgen:
	test -n "$$PSQL_DSN"
	GOCACHE=$(GOCACHE_DIR) go run github.com/stephenafamo/bob/gen/bobgen-psql@v0.45.0 -c ./bobgen.yaml

web-install:
	cd web/app && bun install --frozen-lockfile

web-test:
	cd web/app && bun run test

web-build:
	cd web/app && bun run build

browser-smoke: build
	cd web/app && bun run smoke:browser

web-policy:
	node scripts/supply-chain-policy.mjs

supply-chain: web-install web-policy

release-artifacts: build web-policy

contract:
	GOCACHE=$(GOCACHE_DIR) go run ./cmd/nerocd contract

check: test web-install web-test web-build web-policy contract

run: web-install web-build
	GOCACHE=$(GOCACHE_DIR) go run ./cmd/nerocd server

smoke: build
	NEROCD_ADDR=$${NEROCD_ADDR:-http://127.0.0.1:8080} ./bin/nerocd smoke

docker-up:
	docker compose up --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f
