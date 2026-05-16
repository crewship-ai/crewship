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

FROM node:24-alpine AS frontend
RUN corepack enable pnpm
WORKDIR /app
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN --mount=type=cache,id=pnpm-store,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY . .
RUN pnpm prisma generate
RUN pnpm build

FROM golang:1.26-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,id=go-mod,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
COPY --from=frontend /app/out ./web/out
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
FROM alpine:3.23

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
