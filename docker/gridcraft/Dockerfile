# Build context is the public repository. Supply the verified upstream release
# binary as docker/gridcraft/gridcraft-cli (ignored); see README.md.
# The CLI is a dynamically linked glibc ELF needing only libc, libm and
# libgcc_s; the distroless cc base provides them without a shell or Node.
FROM gcr.io/distroless/cc-debian12@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f
COPY --chmod=0555 docker/gridcraft/gridcraft-cli /usr/local/bin/gridcraft-cli
COPY --chmod=0555 docker/gridcraft/crewship-gridcraft-worker /usr/local/bin/crewship-gridcraft-worker
COPY docker/gridcraft/LICENSE-APACHE docker/gridcraft/LICENSE-MIT /usr/share/licenses/gridcraft/
COPY LICENSE /usr/share/licenses/crewship/LICENSE
USER 1000:1000
WORKDIR /scratch
ENTRYPOINT ["/usr/local/bin/crewship-gridcraft-worker"]
