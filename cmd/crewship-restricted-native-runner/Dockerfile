# Codex159's proc fallback requires the exact Debian-patched bubblewrap 0.8 binary.
# Transport its authenticated package from bookworm; retain its dpkg metadata in
# the final image so vulnerability scanners continue to identify the package.
ARG BASE=debian@sha256:918311b7b6c4c6f68b232ba516584925f6c78ad82b6fd534b98979df6438e483
ARG BWRAP_SOURCE=debian@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
FROM ${BWRAP_SOURCE} AS bubblewrap-package
RUN apt-get update && apt-get download bubblewrap=0.8.0-2+deb12u1 \
    && mv bubblewrap_*.deb /bubblewrap.deb

FROM ${BASE}
COPY --from=bubblewrap-package /bubblewrap.deb /tmp/bubblewrap.deb
RUN apt-get update && apt-get upgrade -y --no-install-recommends \
    && apt-get install -y --no-install-recommends /tmp/bubblewrap.deb ca-certificates ripgrep \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' libc6)" ge '2.41-12+deb13u4' \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' perl-base)" ge '5.40.1-6+deb13u1' \
    && bwrap --version | grep -Fx 'bubblewrap 0.8.0' \
    && echo '85580dd52ed366ece8844e90fa75ac7c4de8802963071344e123221fb9f6f11e  /usr/bin/bwrap' | sha256sum -c - \
    && rm -rf /var/lib/apt/lists/* /tmp/bubblewrap.deb
COPY crewship-restricted-native-runner /opt/crewship-runner
COPY crewship-restricted-native-runner /opt/crewship-native-runner
COPY codex /opt/codex
RUN echo 'd2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f  /opt/codex' | sha256sum -c - \
    && /opt/codex --version | grep -Fx 'codex-cli 0.159.0'
USER 1001:1001
ENTRYPOINT ["/opt/crewship-runner", "hold"]
