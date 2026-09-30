# Codex159's proc fallback requires bubblewrap0.8's /newroot/proc diagnostics.
ARG BASE=debian@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
FROM ${BASE}
RUN apt-get update && apt-get install -y --no-install-recommends bubblewrap=0.8.0-2+deb12u1 ca-certificates ripgrep \
    && bwrap --version | grep -Fx 'bubblewrap 0.8.0' \
    && echo '85580dd52ed366ece8844e90fa75ac7c4de8802963071344e123221fb9f6f11e  /usr/bin/bwrap' | sha256sum -c - \
    && rm -rf /var/lib/apt/lists/*
COPY crewship-restricted-native-runner /opt/crewship-runner
COPY crewship-restricted-native-runner /opt/crewship-native-runner
COPY codex /opt/codex
RUN echo 'd2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f  /opt/codex' | sha256sum -c - \
    && /opt/codex --version | grep -Fx 'codex-cli 0.159.0'
USER 1001:1001
ENTRYPOINT ["/opt/crewship-runner", "hold"]
