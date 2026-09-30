# Restricted native Codex profile

The `native_api_key` profile runs pinned Codex CLI 0.159.0 with its
`workspace-write` sandbox inside a read-only, network-isolated Docker container.
It can use only `exec_command` and `write_stdin` in its isolated scratch space.
Persistent project mounts are not provided by this profile.

The host freezes scoped instructions and input before launch. It replaces the
CLI's ambient instructions, tool schemas, cache identifiers, and metadata. Each
provider call injects the pinned output limit and reserves the known model's
entire input-context ceiling. This accounts conservatively for compressed opaque
reasoning. Every opaque value sent upstream comes from the same attempt's durable
host-observed history. Missing usage retains the full durable reservation.

Scratch output files are captured before teardown: regular non-hidden files only,
no symlinks, at most 32 files, 1MiB per file and 4MiB total. Host ingestion derives
identity and scope from the active attempt and computes immutable content hashes.
The worker has no provider credentials or host filesystem paths.

Operators must explicitly install the reviewed task-owned AppArmor policy with
`sudo scripts/install-restricted-native-policy.sh`, then build the immutable image
with `scripts/build-restricted-native-image.sh /path/to/codex-binary`. The build
checks exact Codex and bubblewrap binary hashes. The host checks the loaded kernel
policy hash and Docker security options at admission, renewal, and delivery. A
missing, changed, unsupported, or unenforced policy denies execution. Installation
does not alter global sysctls, Docker configuration, proc masks, or capabilities.

The Debian base digest and bubblewrap 0.8.0 package are pinned in the Dockerfile.
Bubblewrap 0.12 changes the proc-error path and prevents the pinned CLI's supported
private-PID proc fallback in this Docker environment. Sandbox policy must not be
weakened to compensate. The production image is selected by its immutable SHA.

Legacy `PROVIDER_LOGIN` credentials remain unsupported for restricted native
execution. Official Sign in with ChatGPT uses a separate preview API, typed scoped
tokens, application-managed refresh, and subscription quota. Its preview does not
support `max_output_tokens`; legacy auth files do not prove eligibility or provide
the bounded per-call accounting contract required here. See the official
[SIWC token reference](https://developers.openai.com/siwc/token-sharing-open-source/token-reference) and
[preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).

Acceptance uses synthetic TLS upstreams and real pinned CLI/tool execution; no
paid provider calls. Run the explicit tagged native live canary with
`CREWSHIP_RESTRICTED_LIVE=1 CREWSHIP_RESTRICTED_NATIVE_IMAGE=sha256:... go test -tags restrictedruntime_live ./internal/restricteddispatch -run TestLiveNativeFrozenToolsAndDurableAccounting`.
