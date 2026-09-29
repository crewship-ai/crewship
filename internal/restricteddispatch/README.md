# Application authority → isolated runtime

`Authority` implements the real runtime's `Authority` and `Catalog` interfaces
using `access.Store` and the migrated application database. It replaces a
synthetic allow/deny callback at this boundary. It does **not** enable restricted
chat, CLI, or routines yet.

The authenticated host caller invokes `Prepare` with a trusted command builder.
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

This first connection deliberately supports only offline commands with private,
bounded tmpfs. Every persistent mount is denied, and there are no credential or
network grants. The host command builder is a security boundary: do not wire a
public request or the legacy crew configuration directly into it. A production
provider, grant-aware prompt/recall builder, quota-enforcing storage catalog and
application dispatch adapter remain necessary before enabling restricted access.

`scripts/restricted-runtime-probe/run.sh -race` also runs the build-tagged live
application-authority test: two humans use the same agent, admission reaches
real UID-1001 Docker processes, private temporary data stays separate, and
revoking H1 in the migrated application grant store denies its output and
terminates its container while H2 continues. This is an application-store/runtime
integration test, **not** a live chat/provider acceptance test.
