# Build the static runner with CGO_ENABLED=0 and pass it in this build context.
# Pin BASE to an approved content digest in production deployments.
ARG BASE=alpine@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
FROM ${BASE}
COPY crewship-restricted-runner /opt/crewship-runner
USER 1001:1001
ENTRYPOINT ["/opt/crewship-runner", "hold"]
