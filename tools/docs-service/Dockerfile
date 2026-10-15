FROM node:24.21.0-bookworm-slim
WORKDIR /opt/crewship-docs
RUN corepack enable && corepack prepare pnpm@10.11.0 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --prod --frozen-lockfile
COPY schema.mjs service.mjs healthcheck.mjs ./
USER 1001:1001
ENV NODE_ENV=production
HEALTHCHECK --interval=15s --timeout=15s --start-period=10s --retries=3 CMD ["node", "healthcheck.mjs"]
CMD ["node", "service.mjs"]
