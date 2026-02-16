# Crewship -- crewshipd Production Dockerfile
# Multi-stage build: builder → minimal runtime
# Image: ghcr.io/crewship-ai/crewshipd:latest

FROM golang:1.25-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /crewshipd ./cmd/crewshipd

# -- Runner --
FROM alpine:3.21

RUN apk --no-cache add ca-certificates wget && \
    addgroup -S crewship && adduser -S crewship -G crewship

RUN mkdir -p /var/lib/crewship /var/log/crewship /tmp && \
    chown -R crewship:crewship /var/lib/crewship /var/log/crewship

COPY --from=builder /crewshipd /usr/local/bin/crewshipd

USER crewship

EXPOSE 8080

ENTRYPOINT ["crewshipd"]
