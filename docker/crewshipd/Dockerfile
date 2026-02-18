# Crewship -- Legacy crewshipd Dockerfile (kept for backward compat)
# For new deployments, use the root Dockerfile instead.

FROM golang:1.26-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /crewship ./cmd/crewship

# -- Runner --
FROM alpine:3.23

RUN apk --no-cache add ca-certificates && \
    addgroup -g 1001 -S crewship && adduser -u 1001 -S crewship -G crewship

RUN mkdir -p /var/lib/crewship /var/log/crewship && \
    chown -R crewship:crewship /var/lib/crewship /var/log/crewship

COPY --from=builder /crewship /usr/local/bin/crewship

USER crewship

EXPOSE 8080

ENTRYPOINT ["crewship", "start"]
