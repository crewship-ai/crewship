.PHONY: up down restart status dev dev\:go dev\:next build test lint e2e e2e\:ui

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LICENSE_PUBKEY ?=
LDFLAGS  = -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE) -X github.com/crewship-ai/crewship/internal/license.publicKey=$(LICENSE_PUBKEY)"

# Multi-instance support: crewship_1 -> instance 1, crewship_2 -> instance 2, etc.
INSTANCE_NUM := $(shell basename $(CURDIR) | sed -n 's/^crewship_\([0-9]*\)$$/\1/p')
INSTANCE_NUM := $(or $(INSTANCE_NUM),0)
NEXT_PORT := $(shell [ $(INSTANCE_NUM) -eq 0 ] && echo 3001 || echo $$((3010 + $(INSTANCE_NUM))))

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
	pnpm dev --port $(NEXT_PORT)

dev\:go:
	air

dev\:go-once:
	@set -a && . ./.env.local && set +a && \
	CREWSHIP_LOG_LEVEL=debug \
	go run ./cmd/crewship start

# === Build ===

build:
	pnpm build
	rm -rf web/out && cp -r out web/out
	go build $(LDFLAGS) -o crewship ./cmd/crewship

build\:go:
	go build $(LDFLAGS) -o crewship ./cmd/crewship

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
