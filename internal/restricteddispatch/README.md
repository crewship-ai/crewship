# Application authority → isolated runtime

`Authority` implements the real runtime's `Authority` and `Catalog` interfaces
using `access.Store` and the migrated application database. It replaces a
synthetic allow/deny callback at this boundary. It does **not** enable restricted
chat, CLI, or routines yet.

The authenticated host caller invokes `Prepare` with a trusted command builder.
The caller must also enforce its existing RBAC/capability and origin checks;
resource grants are an additional ceiling, not a replacement for those checks.
Admission happens before the builder reads prompt or memory data. The builder
receives the exact durable attempt, rights, principal and private data scope.
Its command is persisted once, keyed by that attempt, and cannot be updated.
Renewal and output resolve current membership, conversation generation, grants
and the complete delegation chain through the same store. Failed or canceled
preparation revokes the attempt; an attempt without a saved command cannot run.

Construct the runtime Manager with this authority as both authority and catalog,
then pass only the returned opaque handle to `Start`. Neither the handle nor
credential values are saved in `restricted_launches`. Workspace bundles exclude
these execution payloads, as they exclude `access_attempts`.

`Prepare` keeps the offline profile. `PrepareResponses` additionally pins exactly
one explicit `OPENAI_API_KEY` agent grant and the agent's server-configured OpenAI
model before prompt construction. Only active API_KEY credentials at self-service
levels 1/2 are supported. There is no crew/workspace fallback, provider-login
refresh or ambiguous-key selection. The key is decrypted only for the host relay;
`Secrets` never delivers it into the agent process. A child can retain the same
key/model and lower the output ceiling, but cannot acquire the target agent's
different provider credential.

The immutable provider binding is persisted separately from commands and is
excluded from workspace bundles. Credential/grant/model mutations atomically
revoke bound attempts; revoke/regrant between watchdog polls cannot revive an
old handle. Removing a provider binding revokes its attempt, and attaching one
after command preparation is denied. Persistent mounts remain denied.

The host builder remains a security boundary: do not wire a public request or
legacy crew configuration directly into it. Scoped prompt/recall, cumulative
budget enforcement, a quota-enforcing storage catalog and application dispatch
remain necessary before enabling restricted access. This adapter does not yet
support native Codex tool loops or ChatGPT subscription credentials.

`scripts/restricted-runtime-probe/run.sh -race` also runs the build-tagged live
application-authority test: two humans use the same agent, admission reaches
real UID-1001 Docker processes, private temporary data stays separate, and
revoking H1 in the migrated application grant store denies its output and
terminates its container while H2 continues. This is an application-store/runtime
integration test, **not** a live chat/provider acceptance test.
