FROM node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e
WORKDIR /opt/pages
RUN corepack enable && corepack prepare pnpm@10.11.0 --activate
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY build.mjs sdk.ts ./
USER 1001:1001
WORKDIR /work
ENTRYPOINT ["timeout", "--signal=KILL", "120s", "node", "/opt/pages/build.mjs"]
