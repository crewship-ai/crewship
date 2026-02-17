.PHONY: up down restart status dev dev\:go dev\:next build test lint

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

# Start Next.js dev server (port 3001)
dev\:next:
	pnpm dev --port 3001

# Start crewshipd with hot-reload (air watches .go files, auto-rebuilds)
# Requires: go install github.com/air-verse/air@latest
dev\:go:
	air

# Start crewshipd without hot-reload (manual restart)
dev\:go-once:
	@set -a && . ./.env.local && set +a && \
	CREWSHIP_NEXTJS_URL=http://localhost:3001 \
	CREWSHIP_INTERNAL_TOKEN=crewshipd \
	CREWSHIP_STORAGE_BASE_PATH=/tmp/crewship-data \
	CREWSHIP_LOG_PATH=/tmp/crewship-logs \
	CREWSHIP_BOLT_PATH=/tmp/crewship-state/state.db \
	CREWSHIP_LOG_LEVEL=debug \
	go run ./cmd/crewshipd

# Start all services in background (single command)
dev:
	@./dev.sh start

build:
	pnpm build
	go build -o crewshipd ./cmd/crewshipd

test:
	pnpm test
	go test ./...

lint:
	pnpm lint
	go vet ./...
