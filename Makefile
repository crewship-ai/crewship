.PHONY: up down restart status dev dev\:go dev\:next build test lint security sbom notices e2e e2e\:ui validate

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
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed — falling back to go vet. Install with: brew install golangci-lint"; \
		go vet ./...; \
	fi

# === Security (CRE-122) ===

security:
	@echo "→ Running govulncheck..."
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./... || echo "⚠ govulncheck reported issues — see THIRD-PARTY-NOTICES.md for known accepted vulns"; \
	else \
		echo "⚠ govulncheck not installed — go install golang.org/x/vuln/cmd/govulncheck@latest"; \
	fi
	@echo "→ Running gitleaks..."
	@if command -v gitleaks >/dev/null 2>&1; then \
		gitleaks detect --no-banner --redact; \
	else \
		echo "⚠ gitleaks not installed — brew install gitleaks"; \
	fi

sbom:
	@echo "→ Generating SBOMs (SPDX + CycloneDX)..."
	@if command -v syft >/dev/null 2>&1; then \
		syft . -o spdx-json=sbom.spdx.json -o cyclonedx-json=sbom.cdx.json; \
		echo "✓ sbom.spdx.json and sbom.cdx.json generated"; \
	else \
		echo "⚠ syft not installed — brew install syft"; \
	fi

notices:
	@./scripts/gen-notices.sh

e2e:
	pnpm test:e2e

e2e\:ui:
	pnpm test:e2e:ui

validate:
	@./scripts/validate-flow.sh
