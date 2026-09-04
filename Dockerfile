# syntax=docker/dockerfile:1.7
# Crewship -- Single Binary Production Dockerfile
# Multi-stage: Go build + Next.js static export → minimal Alpine image
# Image: ghcr.io/crewship-ai/crewship:latest
#
# BuildKit cache mounts persist the pnpm store and Go module/build cache
# across `docker build` invocations even when the build context layer is
# invalidated, dramatically speeding up incremental builds. Requires
# BuildKit (default on modern Docker; enable with DOCKER_BUILDKIT=1 on
# old daemons) and the syntax directive above.

# Node 22 to match CI (NODE_VERSION: "22" in .github/workflows) and the
# support matrix of the pinned prisma 7.8.0, whose preinstall only accepts
# Node 20.19+/22.12+/24.0+. On node:26-alpine the Prisma CLI's WASM schema
# parser corrupts every schema line to `""` under the emulated linux/arm64
# build, so `pnpm prisma generate` dies with a wall of P1012 "This line is
# invalid" errors even though the schema is valid. Bump only in lockstep
# with CI + a Prisma release that declares Node 26 support.
FROM node:22-alpine AS frontend
# Newer node-alpine images stopped bundling corepack by default, so
# `corepack enable` alone can fail with "corepack: not found". Install it
# explicitly first; `pnpm install` below then picks up the exact version
# pinned in package.json's `packageManager` field, same as CI's
# pnpm/action-setup (see .github/actions/setup-node-pnpm/action.yml).
RUN npm install -g corepack@latest && corepack enable pnpm
WORKDIR /app
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN --mount=type=cache,id=pnpm-store,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY . .
RUN pnpm prisma generate
RUN pnpm build

FROM golang:1.27.0-alpine AS backend
# This tag is the compiler for the shipped binary, and it is the one Go version
# no pull-request check ever exercises: the image is built by release.yml and
# nightly.yml only (#2064). Dependabot's `docker-images` group bumps it alone.
#
# `local` makes the tag above the single authority: the go command uses the
# toolchain this image ships and never downloads another. Without it the
# default is `auto`, which honours go.mod's `toolchain` directive by FETCHING
# that compiler — so a `toolchain` bump would quietly build the release with a
# Go that no line in this repo pins and no vuln scan ever graded.
#
# Deliberately not a version literal. `GOTOOLCHAIN=go1.27.0` would be one more
# copy of the string to keep in sync, and it fails the wrong way round: bump
# the FROM tag above, forget the literal, and Go downloads the OLD toolchain
# and undoes the bump while every check stays green. `local` cannot do that.
#
# What `local` does NOT do — verified against this image, not assumed — is
# object when go.mod's `toolchain` names something else. That directive is
# ignored outright under `local`, so a `toolchain go1.27.1` against this 1.27.0
# tag builds happily and silently with 1.27.0. Only the `go` DIRECTIVE can fail
# the build ("go.mod requires go >= 1.28; running go 1.27.0"), and that line
# deliberately sits at 1.26. So nothing at build time will ever tell us these
# two files disagree — which is exactly why the disagreement is checked
# statically instead, by scripts/go-toolchain-pin.sh, on every PR.
#
# The official golang-alpine images already default to `local`, so this line
# changes nothing today. It is written down because an inherited upstream
# default is a fact that happens to hold, not an invariant: stated here it
# survives a base-image swap, and the guard can check it alongside the FROM
# tag, go.mod's `toolchain` directive and every GO_VERSION in .github/.
ENV GOTOOLCHAIN=local
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,id=go-mod,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
# schemas/ is a root-level package (embed.go + routine.v1.json) imported by
# internal/api and cmd/crewship (#849). It must be copied like any other
# source dir or the in-image `go build ./cmd/crewship` fails to resolve
# github.com/crewship-ai/crewship/schemas (#886). Regular CI hides this
# because it builds from a full checkout; this stage copies dirs selectively.
COPY schemas/ ./schemas/
# config/ is the second such root-level package (embed.go + models.json +
# rate-limits.yml), imported by internal/llm since #2305. Same rule as
# schemas/: it is not under cmd/ or internal/, so it has to be copied by
# name or the in-image build fails to resolve
# github.com/crewship-ai/crewship/config — which is exactly how the
# 2026-09-04 nightly image build broke while every PR check stayed green.
# The PR image build (#2064) now catches this class before merge, and
# scripts/pr-image-build-paths.sh keeps its path filter in step with the
# COPY lines here.
COPY config/ ./config/
COPY web/ ./web/
COPY --from=frontend /app/out ./web/out
# Release gate (#1567). web/out/ now always compiles — a tracked placeholder
# keeps `//go:embed all:out` resolvable in a bare checkout — so the image
# build has to prove the frontend stage actually delivered a real export
# instead of letting a UI-less crewship image ship quietly. scripts/ is not
# copied into this stage, so the check is inlined rather than calling
# scripts/embed-web-out.sh; _next/ is what a Next.js export always emits and
# a placeholder or hand-rolled stub never does.
RUN test -f ./web/out/index.html && test -d ./web/out/_next \
    || (echo "ERROR: web/out/ has no Next.js static export — refusing to build an image with no web UI" >&2; exit 1)
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
# SENTRY_DSN is intentionally a build-arg with empty default. The release
# workflow passes --build-arg SENTRY_DSN=$SENTRY_DSN from the GH secret;
# local `docker build` produces a binary with telemetry hard-off (the
# crashreport package treats empty DSN as "stay disabled regardless of
# opt-in" so dev images never phone home).
ARG SENTRY_DSN=""
# -trimpath strips workspace paths from binary debug info — same
# rationale as the Makefile / goreleaser changes: reproducible builds
# so cosign-verified hashes match across builders.
RUN --mount=type=cache,id=go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE} -X github.com/crewship-ai/crewship/internal/crashreport.DSN=${SENTRY_DSN}" \
    -o /crewship ./cmd/crewship

# -- Runner --
FROM alpine:3.24

RUN apk --no-cache add ca-certificates && \
    addgroup -g 1001 -S crewship && adduser -u 1001 -S crewship -G crewship

RUN mkdir -p /var/lib/crewship /var/log/crewship /data && \
    chown -R crewship:crewship /var/lib/crewship /var/log/crewship /data

COPY --from=backend /crewship /usr/local/bin/crewship
COPY docker/server-entrypoint.sh /usr/local/bin/crewship-entrypoint
RUN chmod +x /usr/local/bin/crewship-entrypoint

USER crewship

EXPOSE 8080

# The wrapper script pre-flight-checks required env vars (NEXTAUTH_SECRET,
# ENCRYPTION_KEY) and prints an actionable error before the binary panics
# deep inside server.New(). `docker run` without these vars now exits 78
# with copy-pasteable fix instructions instead of leaving the user with a
# blank :8080 and a stack trace buried in `docker logs`.
ENTRYPOINT ["crewship-entrypoint"]
