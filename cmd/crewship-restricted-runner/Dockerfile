# Build the static runner with CGO_ENABLED=0 and pass it in this build context.
# The runner is the only program the image runs: the text worker executes
# /opt/crewship-runner directly and talks plain HTTP to the loopback broker, so
# it needs no shell, libc, CA bundle or package manager. An empty base carries
# no distribution packages to patch (#5103: zlib/musl/openssl from alpine).
# A deployment that overrides BASE takes ownership of that image's packages.
ARG BASE=scratch
FROM ${BASE}
COPY crewship-restricted-runner /opt/crewship-runner
USER 1001:1001
ENTRYPOINT ["/opt/crewship-runner", "hold"]
