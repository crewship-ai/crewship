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
qualification. A bounded credential-free process probe checks effective
NoNewPrivs and seccomp under the actual exec UID. Seccomp mode 2 alone does not
attest filter contents.

The selected native Codex executable must have an exact pin and native lock,
immutable environment revision, image, canonical artifact path and SHA-256.
Qualification and execution use the same attested immutable managed launcher and
artifact. A PATH lookup or self-reported CLI version is not executable authority.
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
artifact digest, and qualification outcome. Persistence failure refuses execution.
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
