# Managed Docs engine

This standalone Node 24 service owns document computation, never SQL storage.
It has no database mount, public listener or WebSocket endpoint. D1b exposes it through the authenticated Go Pages bridge. Go
performs live authorization, writer fencing and durable commits for every
operation. Run `pnpm install --frozen-lockfile` and `pnpm test` in this directory.
The package and lockfile are independent of the D0 experiment.

Set `CREWSHIP_DOCS_SOCKET`, `CREWSHIP_DOCS_AUTHORITY_SOCKET` and
`CREWSHIP_DOCS_SERVICE_TOKEN_FILE`; the regular mode-0600 token file must be at most 512 bytes and hold a trimmed secret of 32..256 bytes. All three paths must be distinct, clean absolute paths of at most 100 bytes.
The listener refuses an existing path and creates a mode-0600 Unix socket.
Operators remove a stale socket only after proving its process has exited.
Both processes must be able to access the private socket directory and secret.
Run the container with a read-only root, private `/tmp` tmpfs, no network,
non-root user matching the Go authority UID (image default 1001:1001), bounded memory/CPU/PIDs, and only socket directories and the
read-only token file mounted. The Docker image intentionally publishes no port.

All requests require `X-Crewship-Docs-Service`. Document requests additionally
carry `X-Workspace-ID` and the original `Authorization: Bearer …`; Go decides
whether the live human or agent run has permission, including whether raw
human updates are allowed. Never expose the service directly to users.

`GET /health/live` and `/health/ready` return protocol 1, schema 2 and a SHA-256
build identity over the lockfile, schema and service. `POST /drain` stops new
admission and waits for pending work and lease releases. SIGTERM drains and
closes. Drain waits at most 30 seconds, then reports failure. Document requests are `POST /v1/documents/{page}/{operation}` with JSON:

- `read`: `{}`.
- `initialize`: `{id,schema,body}` with schema-valid JSON content.
- `patch`: `{id,schema,epoch,kind:"human",update}` or
  `{id,schema,epoch,target,text,ifRev,ifHash}` for semantic replaceText.
- `checkpoint`: `{id,schema,epoch,ifRev}`.
- `restore`: `{id,schema,epoch,ifRev,checkpoint}` (checkpoint is a string ID).

The authority protocol is POST `/internal/docs/v1/{page}/{operation}`, forwarding
the same three headers. State, receipt and checkpoint-read are live authorized;
lease/renew/release use opaque epoch-fenced leases. Initialize, commit,
checkpoint and restore return only durable Go acknowledgements. A stable hash
of the original request identifies retries; receipt lookup precedes mutation.
Commit sends an incremental Yjs update, full candidate snapshot, block hashes,
expected revision, generation, writer epoch, owner and opaque lease token. Restore delegates the
new durable epoch to Go. An authority error never causes automatic lease theft.

Admission is bounded to 32 document keys, 128 total operations and 32 per key.
Idle keys unload after 30 seconds. Every operation reloads the Go snapshot and
update log into an isolated Y.Doc, validates it and destroys it afterward.
Bodies are capped at 2 MiB, authority replies at 4 MiB; each authority call has
a 10-second timeout. No content, bearer or service secret is logged.

D1b retains a fenced engine lease across document operations, renews it using
that operation's live principal, and releases it on idle or drain. Every operation
reloads confirmed binary state: unacknowledged content is never a shared cache.
The public editor epoch combines a fresh Go authority identity, Node boot UUID
and durable document generation; it is distinct from the private writer epoch.
Restore/restart therefore resynchronizes editors, while lease acquisition alone
does not. Startup binds the configured socket first, then calls Go's idempotent
service-only recovery barrier. Until that acknowledgement, readiness and document
admission fail closed. A second bind cannot fence the existing engine.

Schema 2 adds bounded lists and headings 1–3. Existing schema-1 documents are
readable and upgrade on a validated commit; future schema versions fail closed.
Comments, checkpoint listing/reading, and Markdown projection use the same bounded
queue and private authority. Comments check generation/revision and live comment
permission transactionally, without acquiring a text writer lease.

At most 128 document HTTP requests are admitted before decoding their bodies;
health and drain remain responsive. Drain aborts incomplete request bodies and
waits for queued operations and lease release, up to 30 seconds. Queue overload
is retryable; permanent document quota refusal retains confirmed history and
reports `document_quota` so the editor can offer recovery without repeated writes.

Mode-0600 socket and secret access requires both processes to run as the same UID.
The image defaults to Go container UID 1001. For a development authority running
as the host user, override the container user with that host UID/GID.

`compose.yaml` is an optional, default-off `docs` profile. Set `DOCS_IMAGE` to a
qualified immutable image reference (for example, a registry image pinned by
`@sha256:…`) after building and qualifying that image. The Compose file does not
build or select an image automatically. Set the required matching UID/GID,
private socket directories and mode-0600 secret file before enabling the profile.

The local image build attempt exited 125 in the shared heavy-job gateway without
build output. D1a subsequently qualified an actual built image in CI. That result applies to
its exact source/image; the changed D1b engine needs its own image workflow run.
Passing native Node and Go process tests alone does not qualify a Docker image.

Node 24's native Unix listener removes its configured socket pathname when it
closes, even if another process replaced that path. A dedicated private socket
directory is therefore a support boundary: operators must not replace or remove
the socket while the service process is alive. There is no custody guarantee
against replacement by another actor able to write that directory. The service
adds no separate unlink on shutdown. Container image smoke qualification is scoped to the workflow below; it does
not qualify an Ansible deployment or published immutable registry image.

The supplemental `Docs foundation` workflow builds this Dockerfile and runs
`DOCS_IMAGE=<local-image> node tools/docs-service/image-smoke.mjs` against that
exact image. The smoke uses a private, controlled authority fixture to exercise
matching-UID mode-0600 access, the packaged healthcheck, unavailable-authority
readiness, drain and graceful container restart with a read-only filesystem and
no network. It does not prove Go durability or power-loss behavior; the separate
real Go/Node process integration covers persistence and process crashes.

The native suite also holds an authority response until explicitly released,
proves the 32-operation per-document queue rejects overflow, and verifies that
drain waits for all admitted failed responses. This is controlled downstream
backpressure and failure injection, not a slow-disk throughput measurement.
