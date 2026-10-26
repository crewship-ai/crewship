# Preserve Codex159's bubblewrap 0.8 proc fallback and authenticated Debian fixes.
# Rebuild the source with a narrow atime-preservation patch; retain dpkg metadata
# and complete modified LGPL source, including the build recipe, in the image.
ARG BASE=debian@sha256:918311b7b6c4c6f68b232ba516584925f6c78ad82b6fd534b98979df6438e483
ARG BWRAP_SOURCE=debian@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
FROM ${BWRAP_SOURCE} AS bubblewrap-package
RUN sed -i 's/^Types: deb$/Types: deb deb-src/' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends build-essential dpkg-dev \
    && apt-get build-dep -y --no-install-recommends bubblewrap=0.8.0-2+deb12u1
WORKDIR /build
RUN apt-get source bubblewrap=0.8.0-2+deb12u1
COPY atime-remount.patch crewship-atime.h Dockerfile /tmp/
RUN cd bubblewrap-0.8.0 \
    && patch -p1 < /tmp/atime-remount.patch \
    && cp /tmp/crewship-atime.h . \
    && cp /tmp/atime-remount.patch debian/crewship-atime.patch \
    && cp /tmp/Dockerfile debian/crewship-build.Dockerfile \
    && printf '%s\n' 'bubblewrap (0.8.0-2+deb12u1+crewship1) bookworm; urgency=medium' '' \
       '  * Preserve existing atime flags during security-only bind remounts.' '' \
       ' -- Crewship contributors <noreply@users.noreply.github.com>  Sun, 25 Oct 2026 00:00:00 +0000' '' \
       > debian/changelog.crewship \
    && cat debian/changelog >> debian/changelog.crewship \
    && mv debian/changelog.crewship debian/changelog \
    && tar --sort=name --mtime=@1792886400 --owner=0 --group=0 --numeric-owner \
       -cJf /bubblewrap-source.tar.xz --transform='s,^,bubblewrap-0.8.0/,' . \
    && SOURCE_DATE_EPOCH=1792886400 DEB_BUILD_OPTIONS=nocheck dpkg-buildpackage -b -us -uc \
    && cp ../bubblewrap_0.8.0-2+deb12u1+crewship1_amd64.deb /bubblewrap.deb

FROM ${BASE}
COPY --from=bubblewrap-package /bubblewrap.deb /tmp/bubblewrap.deb
RUN apt-get update && apt-get upgrade -y --no-install-recommends \
    && apt-get install -y --no-install-recommends /tmp/bubblewrap.deb ca-certificates ripgrep \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' libc6)" ge '2.41-12+deb13u4' \
    && dpkg --compare-versions "$(dpkg-query -W -f='${Version}' perl-base)" ge '5.40.1-6+deb13u1' \
    && bwrap --version | grep -Fx 'bubblewrap 0.8.0' \
    && sha256sum /usr/bin/bwrap \
    && dpkg-query -W -f='${Version}\n' bubblewrap | grep -Fx '0.8.0-2+deb12u1+crewship1' \
    && echo 'c3cd4aa9990c6aee9f084ea6790f4c77056b42262d2a2364073e2295bf6d855f  /usr/bin/bwrap' | sha256sum -c - \
    && rm -rf /var/lib/apt/lists/* /tmp/bubblewrap.deb
COPY crewship-restricted-native-runner /opt/crewship-runner
COPY crewship-restricted-native-runner /opt/crewship-native-runner
COPY codex /opt/codex
RUN echo 'd2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f  /opt/codex' | sha256sum -c - \
    && /opt/codex --version | grep -Fx 'codex-cli 0.159.0'
COPY --from=bubblewrap-package /bubblewrap-source.tar.xz /usr/share/doc/bubblewrap/crewship-source.tar.xz
USER 1001:1001
ENTRYPOINT ["/opt/crewship-runner", "hold"]
