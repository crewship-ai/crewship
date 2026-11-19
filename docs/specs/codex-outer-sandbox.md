# Crew-scoped Codex outer isolation

This contract defines an explicit alternative to Codex's nested namespace sandbox.
It does not enable the mode by default or qualify the existing global override.
The filesystem and process trust boundary is the entire crew, not each agent.

## Consent and selection

`allow_codex_outer_sandbox` is a persisted crew boolean, default false on new and
upgraded installations. Only workspace OWNER/ADMIN may change it through the
crew PATCH endpoint or `crew update --allow-codex-outer-sandbox=true|false`.
Authorization is rechecked in the writer transaction. Audit records the actor,
crew, previous value and new value. Dispatch reads current stored consent scoped
by workspace and crew; prompt text, agent env and public run-request fields
cannot authorize a reduced nested sandbox.

Codex normally receives `workspace-write`. MINIMAL always receives `read-only`.
Only a non-MINIMAL Codex run with explicit consent and successful qualification
receives `danger-full-access`. Other adapters and crews retain their selection.
Revocation governs new executions; it cannot retrofit a sandbox into a running
process. Missing or revoked consent before creation refuses the attempt.

The legacy server variable `CREWSHIP_CODEX_SANDBOX_MODE=danger-full-access` is
not authority. Without crew consent it causes an actionable refusal directing
the operator to remove the override or opt in and qualify the selected crew.
With consent it follows the same qualification as every other outer-isolated
run. It never overrides MINIMAL. This is an intentional compatibility change.

## Runtime and executable evidence

A retained runtime-use reservation binds the concrete container throughout
qualification and execution. The provider inspects that actual container,
not just the requested configuration. Unsupported providers refuse the mode.
Required evidence includes crew/installation ownership, immutable selected image,
UID 1001, read-only rootfs, no privileged mode, CapDrop ALL, no added capabilities
or devices, no-new-privileges, private PID/IPC/network namespaces, no volumes-from,
no unconfined security profile, and only approved crew mounts. Docker sockets,
host-sensitive binds and writable overlays of executable/launcher code refuse
qualification. The only accepted binds are read-only: the trusted launcher, the
entrypoint, and, when the installation stages one, the immutable native agent
CLI at `/usr/local/bin/crewship` from that exact staged artifact (required then,
refused otherwise). The existing trusted static launcher checks effective UID, GID, NoNewPrivs,
seccomp and zero capabilities in its own process immediately before exec.
It creates no additional probe process or RunID. Seccomp mode 2 alone does not
attest filter contents.

The selected native Codex executable must have an exact pin and native lock,
immutable environment revision, image, canonical artifact path and SHA-256.
Image-bound startup qualification and execution select the same attested
immutable artifact. The trusted launcher validates and executes the same open
file descriptor. Startup qualification alone does not prove shell functionality;
the release E2E fixture must execute the actual Codex shell tool. A PATH lookup or self-reported CLI version is not executable authority.
Existing mutable launcher binds require runtime recreation, and images missing
artifact evidence require reprovisioning. No silent fallback to a legacy launch
is allowed. The managed-launch static-native limitations apply; this mode does
not expand support to dynamic ELF or shell wrappers.

The existing managed-launch pilot restricts entire crews to Codex. This mode
qualifies the selected Codex artifact while retaining other adapters in mixed
crews; it does not switch their providers or broaden that pilot's guarantees.
Immediately before the creation gate, stored consent, immutable build authority
and actual runtime are rechecked. Changed evidence refuses execution.

## Preflight and audit

Preflight is bounded, uses no credential values and makes no paid model request.
Audit carries crew, agent, run, selected mode, consent source, immutable image and
artifact digest, and qualification outcome. `admitted` records successful host attestation and stored
consent, with effective kernel checks still enforced by the launcher before
Codex executes; it is not a successful-shell receipt. Persistence failure
refuses execution.
No credential value, provider token or material from a private key enters the
audit. Approval to use outer isolation does not prove model authentication or a
successful shell execution.

No host AppArmor/seccomp/sysctl policy is weakened, no privileged container is
introduced, and no server-wide unsandboxed mode is enabled by this contract.
The installer and runtime diagnostics must explain an unsupported namespace
configuration before a model is asked to use a shell.

## Regression acceptance

Tests prove default-off behavior after fresh migration and upgrade; OWNER/ADMIN
changes and transaction-time authorization; unchanged other crews/adapters;
MINIMAL read-only selection; explicit legacy-variable refusal; each missing
runtime proof; changed artifact/image/consent before exec; and mixed crews.
A real qualified container with the exact Codex executable and a local provider
fixture must execute a shell command. The same fixture must refuse an
unqualified container. A model response or COMPLETED run state is insufficient.

## Child tool environment

The static launcher uses its fixed system PATH plus `/opt/crew-tools/bin` and
`/opt/mise/data/shims` only for this mode. Host admission supplies the platform's
`MiseRuntimeEnv` constants; image PATH and loader/shell hooks remain excluded.
The main Codex still executes through the verified FD even if a same-named child
program exists in the tools path. Child tools are arbitrary code within the crew
trust boundary: `/opt/crew-tools` is writable and its tools are not attested.
These fixed paths do not cover every devcontainer feature's custom bin directory;
no complete feature-tool availability is claimed. The original managed pilot's
fixed system PATH and environment contract are unchanged.

For the real native Docker release fixture, provide an explicit static artifact
and an already cached `alpine:3` image. The test never installs a CLI or reads a
provider login:

```bash
go test -tags integration ./internal/orchestrator \
  -run '^TestCodexOuterActualNativeDocker$' -count=1 -v \
  -args -codex-outer-native-artifact=/absolute/path/to/native/codex
```

An absent artifact fails the selected fixture, and a refused Docker gateway is
an unmet test precondition. Neither is a successful release qualification.
The required CI conformance lane provides the pinned native 0.169.0 artifact
and rejects missing, skipped or failed fixture execution. A successful receipt
includes the actual native version, immutable image ID and artifact SHA-256.
