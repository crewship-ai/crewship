FROM node:24-trixie-slim@sha256:7f486cabe892bff8fef04c0f7da8dba84a560be3e4e2740b7a86afc57ebec315 AS build
WORKDIR /opt/slides-export
RUN corepack enable && corepack prepare pnpm@10.11.0 --activate
COPY tools/slides-export/runtime/package.json tools/slides-export/runtime/pnpm-lock.yaml tools/slides-export/runtime/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY tools/slides-export/runtime/worker.mjs tools/slides-export/runtime/entry.mjs tools/slides-export/runtime/profile.mjs tools/slides-export/runtime/profile.go tools/slides-export/runtime/profile.json tools/slides-export/runtime/Dockerfile ./
COPY tools/slides-export/runtime/fonts/DejaVuSans.ttf tools/slides-export/runtime/fonts/LICENSE-DejaVu.txt tools/slides-export/runtime/fonts/provenance.json ./fonts/
# This build-only legal collector is fingerprinted and verified before use.
COPY scripts/gen-frontend-licenses.mjs /tmp/export-license-generator.mjs
COPY internal/slidesexport/input.go /tmp/export-admission.go
RUN node profile.mjs --verify-admission /tmp/export-admission.go \
    && node profile.mjs --verify-license-generator /tmp/export-license-generator.mjs \
    && node /tmp/export-license-generator.mjs /licenses

FROM node:24-trixie-slim@sha256:7f486cabe892bff8fef04c0f7da8dba84a560be3e4e2740b7a86afc57ebec315
COPY --from=build /opt/slides-export /opt/slides-export
COPY --from=build /licenses /usr/share/doc/crewship-slides-export/licenses
USER 1001:1001
WORKDIR /tmp
ENTRYPOINT ["timeout", "--signal=KILL", "120s", "node", "--max-old-space-size=512", "/opt/slides-export/entry.mjs"]
