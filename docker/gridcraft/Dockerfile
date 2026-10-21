# Build context is the public repository. Supply the verified upstream release
# binary as docker/gridcraft/gridcraft-cli (ignored); see README.md.
FROM node:24-bookworm-slim@sha256:8cabefbd444809cf0e319b5831c65da1ea202200ca552a5e75fd7938a6f86658
COPY --chmod=0555 docker/gridcraft/gridcraft-cli /usr/local/bin/gridcraft-cli
COPY --chmod=0555 docker/gridcraft/crewship-gridcraft-worker /usr/local/bin/crewship-gridcraft-worker
COPY docker/gridcraft/LICENSE-APACHE docker/gridcraft/LICENSE-MIT /usr/share/licenses/gridcraft/
COPY LICENSE /usr/share/licenses/crewship/LICENSE
USER 1001:1001
WORKDIR /scratch
ENTRYPOINT ["/usr/local/bin/crewship-gridcraft-worker"]
