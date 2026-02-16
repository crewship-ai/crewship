# Crewship -- Next.js Production Dockerfile
# Multi-stage build: deps → builder → runner
# Image: ghcr.io/crewship-ai/crewship:latest

FROM node:22-alpine AS base
RUN corepack enable pnpm

# -- Dependencies --
FROM base AS deps
WORKDIR /app
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

# -- Build --
FROM base AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .

ARG DATABASE_URL="postgresql://placeholder:placeholder@localhost:5432/placeholder"
ENV DATABASE_URL=${DATABASE_URL}

RUN pnpm prisma generate
RUN pnpm build

# -- Runner --
FROM base AS runner
WORKDIR /app

ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1

RUN addgroup --system --gid 1001 crewship && \
    adduser --system --uid 1001 crewship

COPY --from=builder /app/public ./public
COPY --from=builder --chown=crewship:crewship /app/.next/standalone ./
COPY --from=builder --chown=crewship:crewship /app/.next/static ./.next/static
COPY --from=builder /app/lib/generated/prisma ./lib/generated/prisma
COPY --from=builder /app/prisma ./prisma
COPY docker/docker-entrypoint.sh /app/docker-entrypoint.sh

USER crewship

EXPOSE 3000
ENV PORT=3000
ENV HOSTNAME="0.0.0.0"

CMD ["node", "server.js"]
