.PHONY: up down restart status dev dev\:go dev\:next build build\:go build\:sidecar test cover lint security sbom notices e2e e2e\:ui validate smoke-cli

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LICENSE_PUBKEY ?=
# Coverage knobs for `make cover` (see the Test & Lint section).
COVER_PKGS   ?= ./...
# coverage/ is already gitignored, so the profile never lands in a commit.
COVERPROFILE ?= coverage/go-cover.out
# SENTRY_DSN is the Go-side crash-report endpoint, baked in at link time
# via -X internal/crashreport.DSN. Empty default keeps local `make build`
# fully telemetry-silent; CI/release paths export the real value from the
# SENTRY_DSN GitHub Actions secret. Mirrors .goreleaser.yml + Dockerfile so
# all three build paths agree on the contract. The frontend equivalent
# (NEXT_PUBLIC_SENTRY_DSN, routed to the SENTRY_DSN_FRONTEND secret in CI)
# is consumed directly by `pnpm build` from process env — no wiring needed
# here as long as it's exported when `make build` is invoked.
SENTRY_DSN ?=
# SIDECAR_BUILD_HASH — content hash (sha256, first 12 hex chars — same shape
# as internal/sidecar.selfExeHash) of the freshly built ./crewship-sidecar,
# baked into the crewship binary so the server knows which sidecar it was
# built alongside (#1160). Lets stale-sidecar detection catch ARTIFACT
# staleness (deploy updated the server but forgot build:sidecar + copy —
# runtime on-disk hashing compares old-vs-old and stays silent).
# Recursively expanded (=, not :=) ON PURPOSE: the $(shell) runs when the
# build recipe line expands — i.e. AFTER the build:sidecar prerequisite wrote
# the file — not at makefile-read time. Missing file → empty → the server
# falls back to on-disk hashing (fail-open), never a false alarm.
# The pipeline shape is locked by TestSidecarHashShellContract
# (internal/provider/docker/sidecar_binhash_test.go).
SIDECAR_BUILD_HASH = $(shell (sha256sum crewship-sidecar 2>/dev/null || shasum -a 256 crewship-sidecar 2>/dev/null) | cut -c1-12)
LDFLAGS    = -ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE) -X github.com/crewship-ai/crewship/internal/license.publicKey=$(LICENSE_PUBKEY) -X github.com/crewship-ai/crewship/internal/crashreport.DSN=$(SENTRY_DSN) -X github.com/crewship-ai/crewship/internal/provider/docker.buildExpectedSidecarHash=$(SIDECAR_BUILD_HASH)"
# -trimpath strips workspace paths (/Users/.../crewship_2/...) from the
# binary, giving reproducible builds across machines and shaving a few KB
# off the embedded debug info. Adds nothing measurable to compile time.
GO_BUILD   = go build -trimpath $(LDFLAGS)

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

build: build\:sidecar
	pnpm build
	rm -rf web/out && cp -r out web/out
	$(GO_BUILD) -o crewship ./cmd/crewship

build\:go: build\:sidecar
	$(GO_BUILD) -o crewship ./cmd/crewship

# Build the crewship-sidecar binary as a standalone executable and stage
# entrypoint.sh next to it. This lets users bring their own base image
# (debian, ubuntu, alpine-glibc, etc.) while reusing the baked-in sidecar
# via a host bind mount. See internal/provider/docker/docker.go buildMounts.
#
# ALWAYS GOOS=linux (#953): the sidecar is bind-mounted into the crew's
# LINUX container and exec'd there, so it must be a Linux ELF for the
# container's arch — NOT host-native. On macOS a native build produces a
# Mach-O binary that fails in-container with exit 255 (misreported as a
# glibc/musl mismatch). Mirrors dev.sh's arch mapping.
# Host-arch → GOARCH mapping WITHOUT $(shell case …): GNU Make terminates
# $(shell) at the first unbalanced ")" — the one closing "aarch64)" — so
# the shell got an unterminated `case` and the recipe got the leaked rest
# ("Syntax error: end of file unexpected"). Broke every `make build:sidecar`
# since the case expression landed; CI never runs this target, deploys do.
UNAME_M := $(shell uname -m)
ifeq ($(UNAME_M),arm64)
  SIDECAR_GOARCH := arm64
else ifeq ($(UNAME_M),aarch64)
  SIDECAR_GOARCH := arm64
else ifeq ($(UNAME_M),x86_64)
  SIDECAR_GOARCH := amd64
else ifeq ($(UNAME_M),amd64)
  SIDECAR_GOARCH := amd64
else
  SIDECAR_GOARCH := $(shell go env GOARCH)
endif
build\:sidecar:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(SIDECAR_GOARCH) go build -trimpath -ldflags="-s -w" -o crewship-sidecar ./cmd/crewship-sidecar
	cp scripts/entrypoint.sh ./entrypoint.sh
	chmod +x ./entrypoint.sh

# === Test & Lint ===

test:
	pnpm test
	go test ./...

# Repo-wide Go coverage profile + total, in one reproducible command.
# -timeout 40m: the full run measures ~4m but `go test` applies its
# per-package timeout to the whole binary set here; the 25m default is what
# ad-hoc coverage runs kept tripping over. COVER_PKGS/COVERPROFILE are
# overridable for a narrower measurement, e.g.
#   make cover COVER_PKGS=./internal/api/...
cover:
	@mkdir -p $(dir $(COVERPROFILE))
	go test $(COVER_PKGS) -coverprofile=$(COVERPROFILE) -covermode=atomic -timeout 40m
	@echo "→ Total coverage:"
	@go tool cover -func=$(COVERPROFILE) | tail -1

# Regenerates internal/orchestrator/testdata/cli-fixtures/*.ndjson from the
# REAL upstream CLI binaries — internal/orchestrator/e2e_multi_cli_test.go has
# cited this target since it was written, but it never existed until now
# (2026-07-21 quality-testability audit). NOT part of `test`/CI: it needs all
# six CLIs installed, spends real API quota per adapter, and each upstream
# tool changes independently — run it manually or from a nightly dev2 job,
# never per-PR. Missing CLIs are skipped with a warning, not a failure, so a
# partial local install can still refresh the fixtures it has.
#
# opencode.ndjson is currently authored from the documented schema (#943), not
# captured — this target replaces it with a real capture the first time
# opencode is installed where it runs.
smoke-cli:
	@mkdir -p internal/orchestrator/testdata/cli-fixtures
	@if command -v claude >/dev/null 2>&1; then \
		echo "→ capturing claude.ndjson"; \
		claude --print --output-format stream-json "say hello" \
			> internal/orchestrator/testdata/cli-fixtures/claude.ndjson; \
	else echo "⚠ claude not installed — skipping claude.ndjson"; fi
	@if command -v codex >/dev/null 2>&1; then \
		echo "→ capturing codex.ndjson"; \
		codex exec --json --sandbox read-only -- "say hello" \
			> internal/orchestrator/testdata/cli-fixtures/codex.ndjson; \
	else echo "⚠ codex not installed — skipping codex.ndjson"; fi
	@if command -v gemini >/dev/null 2>&1; then \
		echo "→ capturing gemini.ndjson"; \
		gemini -p "say hello" --output-format stream-json \
			> internal/orchestrator/testdata/cli-fixtures/gemini.ndjson; \
	else echo "⚠ gemini not installed — skipping gemini.ndjson"; fi
	@if command -v opencode >/dev/null 2>&1; then \
		echo "→ capturing opencode.ndjson"; \
		opencode run --format json -- "say hello" \
			> internal/orchestrator/testdata/cli-fixtures/opencode.ndjson; \
	else echo "⚠ opencode not installed — skipping opencode.ndjson (stays hand-authored from #943 docs)"; fi
	@if command -v cursor-agent >/dev/null 2>&1; then \
		echo "→ capturing cursor.ndjson"; \
		cursor-agent -p --output-format stream-json --force -- "say hello" \
			> internal/orchestrator/testdata/cli-fixtures/cursor.ndjson; \
	else echo "⚠ cursor-agent not installed — skipping cursor.ndjson"; fi
	@if command -v droid >/dev/null 2>&1; then \
		echo "→ capturing droid.ndjson"; \
		droid exec --auto low -o stream-json -- "say hello" \
			> internal/orchestrator/testdata/cli-fixtures/droid.ndjson; \
	else echo "⚠ droid not installed — skipping droid.ndjson"; fi
	@echo "→ done. Review the diffs, then commit + bump pinnedNpmVersion in internal/orchestrator/cli_adapter_versions_test.go if any schema changed."

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
