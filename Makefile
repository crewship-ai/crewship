.PHONY: up down restart status dev dev\:go dev\:next build test lint e2e e2e\:ui

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS  = -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

# === Quick aliases (recommended) ===

up:
	@./dev.sh start

down:
	@./dev.sh stop

restart:
	@./dev.sh restart

status:
	@./dev.sh status

# === Individual services (advanced) ===

dev\:next:
	pnpm dev --port 3001

dev\:go:
	air

dev\:go-once:
	@set -a && . ./.env.local && set +a && \
	CREWSHIP_NEXTJS_URL=http://localhost:3001 \
	
	CREWSHIP_STORAGE_BASE_PATH=/tmp/crewship-data \
	CREWSHIP_LOG_PATH=/tmp/crewship-logs \
	CREWSHIP_BOLT_PATH=/tmp/crewship-state/state.db \
	CREWSHIP_LOG_LEVEL=debug \
	go run ./cmd/crewshipd

# === Build ===

build:
	pnpm build
	rm -rf web/out && cp -r out web/out
	go build $(LDFLAGS) -o crewship ./cmd/crewship

build\:go:
	go build $(LDFLAGS) -o crewship ./cmd/crewship

build\:legacy:
	go build $(LDFLAGS) -o crewshipd ./cmd/crewshipd

# === Test & Lint ===

test:
	pnpm test
	go test ./...

lint:
	pnpm lint
	go vet ./...

e2e:
	pnpm test:e2e

e2e\:ui:
	pnpm test:e2e:ui
