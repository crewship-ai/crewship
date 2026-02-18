# Crewship -- Single Binary Production Dockerfile
# Multi-stage: Go build + Next.js static export → minimal Alpine image
# Image: ghcr.io/crewship-ai/crewship:latest

FROM node:24-alpine AS frontend
RUN corepack enable pnpm
WORKDIR /app
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm prisma generate
RUN pnpm build

FROM golang:1.26-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
COPY --from=frontend /app/out ./web/out
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /crewship ./cmd/crewship

# -- Runner --
FROM alpine:3.23

RUN apk --no-cache add ca-certificates && \
    addgroup -g 1001 -S crewship && adduser -u 1001 -S crewship -G crewship

RUN mkdir -p /var/lib/crewship /var/log/crewship /data && \
    chown -R crewship:crewship /var/lib/crewship /var/log/crewship /data

COPY --from=backend /crewship /usr/local/bin/crewship

USER crewship

EXPOSE 8080

ENTRYPOINT ["crewship", "start"]
