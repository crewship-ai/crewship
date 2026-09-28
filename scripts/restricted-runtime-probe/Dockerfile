# The harness pins the locally inspected base image by its content ID.
ARG BASE=alpine:3
FROM ${BASE}
COPY runner /opt/crewship-runner
COPY sidecar /opt/crewship-sidecar
USER 1001:1001
ENTRYPOINT ["/opt/crewship-runner", "hold"]
