# Changelog

All notable changes to Crewship are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 releases may introduce breaking changes in minor versions
(`0.x.0`); patch releases (`0.x.y`) are backwards-compatible fixes.

## [Unreleased]

### Added

- **Tabs on a page (#1935).** A panel may declare `tab: Odezva`, and the page
  grows a bar under the breadcrumb — several screens instead of one long
  scroll. One optional key on the panel and **no `tabs:` block**: adding a tab
  is one word on the panel that needs it, and there is no second list that can
  disagree with the panels about which of them exists. Bar order is first
  appearance in the panel list; a panel that declares none lands on the first
  tab; a page where nothing declares one has no bar and renders exactly as it
  did before.

  A tab HIDES panels, which is why this is not only a layout feature. Every tab
  carries the **worst freshness state of its own panels** — `failed` over
  `stale` over `never_produced` over `fresh` — as a glyph beside its name and
  never as colour alone, and the page's own freshness summary is computed over
  **every** tab rather than the visible one, so it does not move when the tab
  does. Without both, a critical panel could sit failing on the third tab while
  the page read fine, which is the silent-old-numbers failure the freshness
  contract exists to prevent.

  A tab whose panels are all sealed to a reader still appears on that reader's
  bar, carrying its placeholders: the page has the same shape for everyone, and
  a bar that reflowed per viewer would disclose, by what it left out, whose data
  was on it. `tab` therefore rides on the sealed placeholder too — page
  structure, like `span`, never the panel's data.

  The selected tab is in the URL (`/pages/sit?tab=odezva`). **Print ignores
  tabs**: paper cannot be clicked, so a printed page renders every tab's panels
  in bar order under their tab names, with no bar. On a phone the bar scrolls
  sideways rather than wrapping. A tab name that is blank, longer than 32
  characters, carries a control character, collides with another differing only
  in case, or is the ninth tab on a page, is refused at save with the reason.

  Carried end to end: YAML → CLI → API → the manifest kind (including drift
  detection, on sealed panels too) → `page export`/`import` → the in-app editor.
  The editor was **dropping `icon:` on every save** the same way it would have
  dropped `tab:` — `PATCH` replaces the panel set wholesale, so a key the editor
  does not mention is a key the save deletes — and both now round-trip.

- **Attachments on issues — and the agent working the issue can read them
  (#1768).** Attach a crash log, a screenshot, a repro bundle or a diff to an
  issue over the API (`GET`/`POST` `…/issues/{ident}/attachments`,
  `GET`/`DELETE` `…/{attachmentId}`), over the CLI (`crewship issue attach` /
  `attachments` / `attachment` / `detach`), or from an agent through its
  sidecar (`GET`/`POST /issue/{ident}/attachments`,
  `GET /issue/{ident}/attachments/{id}`). The agent half is the point: a file
  an agent cannot read is decoration, and until now the only way one reached an
  agent was a human pasting its contents into a comment. Every attach and detach
  goes through the shared issue-event emitter, so it lands on the timeline **and**
  in the journal — which is what makes it notifiable.

  Content reaching an agent is treated as attacker-controlled: both the file's
  **text** and its **filename** are wrapped by `internal/untrusted` before they
  leave the handler (`ignore previous instructions.txt` is a shorter payload than
  the file, and it shows up in every listing). Binary comes back base64, budgeted
  at 512 KiB, text at 128 KiB, both with an explicit `truncated` flag — a silent
  truncation is worse than a refusal.

  The type is resolved from the file's **extension** against an allowlist; the
  request's own `Content-Type` is discarded, because honouring it is how a stored
  file becomes stored XSS served from your own origin. `.html` and `.svg` are
  absent deliberately. Downloads carry the resolved type plus `nosniff` and
  `Content-Disposition: attachment`.

  Blobs are content-addressed at
  `attachments/<workspace>/<sha[0:2]>/<sha>` — every component derived from bytes
  we computed, so a filename of `../../../etc/passwd` is stored as the label
  `passwd` and path traversal is not expressible rather than merely refused.
  Identical bytes are de-duplicated **within a workspace and never across one**:
  a cross-tenant shared blob would make workspace erasure undecidable and would
  turn write-time de-duplication into an existence oracle. Deletion is
  reference-counted, so removing a file from one issue never removes it from
  another that carries the same file. Cascade deletes (an issue hard-deleted)
  never reach that refcount, so `crewship issue delete` runs a reclaim pass
  derived purely from the table.

### Changed

- **`OTEL_EXPORTER_OTLP_ENDPOINT` is treated as the base URL it is, and
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` is honoured (#1870).** The standard
  says the generic variable is a base URL for every signal and the signal
  path is appended to it — including when it already carries a path. We
  appended only when there was no path, and ignored the signal-specific
  variable entirely.

  Two consequences. A backend documenting a project-scoped prefix, such as
  Langfuse's `/api/public/otel`, expects spans at
  `/api/public/otel/v1/traces`; we posted to the bare prefix, where a
  collector answers 200 and drops the payload — indistinguishable from
  working. And an operator who set `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, the
  correct way to pin an exact URL, was not listened to at all.

  **If you configured a bare prefix, traces move** — to where the backend
  wanted them. A path that already ends in `/v1/traces` is not doubled, so a
  fully-written-out endpoint is unaffected. To pin any exact URL, use
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`.

  The startup line now prints `traces_url` — the resolved destination, signal
  path included — instead of the configured endpoint. Logging what was
  configured is what let a misrouted exporter read as healthy for a release.

- **The OTLP endpoint keeps its own `/v1/traces` default.** opentelemetry-go
  1.45 changed `WithEndpointURL`: a URL with no path of its own used to fall
  through to the exporter's default signal path, and now resolves to `/`. Since
  `OTEL_EXPORTER_OTLP_ENDPOINT` is a base URL — that is the standard, and what
  our setup guide tells you to configure — every collector would have started
  receiving spans on its root route, where a 200 and a discarded payload look
  exactly like success. `telemetry.Init` appends the signal path itself now, so
  the upgrade is invisible to operators and the SDK's default stops mattering
  to us. An endpoint that already carries a path keeps it verbatim, as before.

  The setup guide also claimed the SDK appends `/v1/traces` to a project-scoped
  prefix such as `/api/public/otel`. It never did, in any version — if you
  configured a bare prefix, set the full URL your backend documents.
- **WAL checkpointing moved off the request path.** Every commit appends
  frames to the `-wal` sidecar, and SQLite folds them back inside whichever
  write transaction happens to cross the threshold — so the cost landed on a
  random agent. The daemon now disables the inline autocheckpoint
  (`database.WithManagedWAL`) and runs a dedicated checkpointer that does the
  work on a goroutine nobody is waiting on. At 100 concurrent agents, three
  runs per policy, p99 write latency went **26.1 ms → 8.9 ms** and the WAL
  ended at 0 instead of 13.6 MB.

  Two findings are recorded in `internal/database/checkpoint.go` because both
  contradict the obvious guess: a `PASSIVE`-only checkpointer is **worse than
  doing nothing** (it folds frames back but never resets the `-wal` file, which
  grew to 98 MB), and the win comes from *who pays* for the checkpoint, not
  from checkpointing more often. Only the long-lived daemon disables
  autocheckpoint; short-lived CLI commands keep SQLite's built-in behaviour,
  and the pairing is enforced by a test.

- **The port-exposure purge no longer stalls every writer.** The sweeper runs
  every 30 s and issued one unbounded `UPDATE`, so its cost scaled with the
  backlog: measured at 41 ms of held write lock for 5,000 rows and **486 ms
  for 50,000**, with a concurrent live write blocked for 449 ms. That is the
  same sweeper whose lock hold took logins down on 2026-05-25 and is why
  `busy_timeout` was raised to 30 s. It now drains in bounded batches,
  releasing the lock between each, and logs the remaining backlog if it ever
  stops at its iteration cap.

  Bounding is right here because the job is *frequent* and its backlog grows.
  It is the wrong move for a large infrequent job: chunking a daily
  20,000-row sweep measured **worse** (150 ms → 405 ms total, live-writer p95
  4.1 ms → 21.5 ms), because re-acquiring the write lock costs more than the
  shorter holds save. Both numbers are in the code comment so the pattern is
  not copied blindly.

- **The two audit tables that grew forever now have retention windows
  (#1887).** `credential_audit` and `audit_logs` were the only tables in the
  schema with no pruning at all — `pipeline_runs`, `inbox_items` and
  `journal_entries` are all swept, these two never were. `credential_audit`
  gains a row on every credential read by every agent; on a dev instance with
  almost no real use it was already the second-largest table in the database.

  The defaults differ deliberately. `credential_audit` keeps **90 days**,
  matching the pipeline-run window — it is operational telemetry, and the
  answer an operator actually reads (`last_used_at`, the `last_used_ips` ring)
  lives on `credentials` and is untouched. `audit_logs` keeps **everything**,
  because it is the compliance trail and `docs/security/gdpr.mdx` says audit
  records have to survive the operator's own retention obligations. Crewship is
  self-hosted: that duty is theirs to know, so the mechanism ships and the
  decision does not. Both are overridable per workspace, where `NULL` means
  "use the default" and `0` means an explicit "keep forever" — a distinction
  `run_retention_days` does not make, because nobody has a legal duty to retain
  a pipeline run.

  Upgrading never deletes anything on its own: the migration pins every
  workspace that already existed to an explicit "keep forever", so the 90-day
  default only ever applies to workspaces created after it. Otherwise the first
  restart after the upgrade would have swept a year of credential history away
  before the API that sets the override was even listening.

  The sweep deletes in bounded batches so no single statement can stall every
  writer, and says how much backlog is left if it stops at its cap. Since
  `audit_logs` is unlimited by default, it also warns when a workspace has no
  window and the table passes a million rows — an operator should hear about
  that from a log line, not from a full disk.

- **The credential audit view stops sorting the whole table to answer one
  page (#1889).** `credential_audit` had no `workspace_id`, so scoping it to a
  tenant meant joining through `credentials` — a shape no index could serve.
  SQLite either walked the table in time order probing `credentials` for every
  row, or gathered the entire matching set and built a temporary B-tree to sort
  it before `LIMIT` threw almost all of it away; the page's `COUNT(*)` then ran
  the same join again. That cost grows with the table, and this is the one
  audit table with no retention sweep at all.

  The column is now carried directly, with
  `(workspace_id, occurred_at DESC)` behind it, matching how `audit_logs` and
  `keeper_request_events` have always worked. The single writer derives the
  value from the credential inside the same `INSERT`, so it cannot disagree
  with the row it describes and no caller has to know the workspace to write an
  audit row. Existing rows are backfilled. No behaviour change — the same rows
  come back.

- **Sixteen foreign keys indexed, thirty-two deliberately not (#1890).** With
  foreign keys enforced, deleting a parent row scans every child table whose
  referencing column does not lead an index — once per deleted row, holding the
  write lock. 48 columns were in that state. The ones now indexed are those
  where the parent is genuinely hard-deleted *and* the child table grows;
  `DELETE FROM missions WHERE crew_id = ?` drove three of them on its own,
  because it deletes many missions in one statement and re-checks every child
  of `missions` for each.

  The 32 skipped are the interesting half. Nine reference `users`, and nothing
  in the tree hard-deletes a user row — so an index there is write cost with no
  delete to accelerate. The rest are settings and small config tables where a
  scan costs nothing. Both exclusions are pinned by tests, so they stay
  decisions rather than oversights, and a ratchet makes the remaining count
  visible if a new table adds foreign keys nobody sized.

- **A task whose assignment link failed is now FAILED, not stranded (#1892).**
  `scheduleTask` inserted the assignment and then linked it to the task in two
  separate statements. A failure between them logged a warning and reported
  success, leaving the task `IN_PROGRESS` with no assignment — a state
  `resolveReadyFromTasks` never re-picks, so the work sat there forever with no
  operator-visible symptom. Both writes are now one transaction, and a link
  failure surfaces as a failed task. The dependent-task cascades
  (`unblockDependentTasks`, `failDependentTasksRecurse`) also stopped issuing
  one standalone `UPDATE` per row: each level is now a single batched
  transaction, so a mid-cascade failure unblocks nobody instead of half the
  graph.

- **`presence.Track` no longer fabricates a transition.** The prior status was
  read and the new one written in two statements, so a losing writer could emit
  `online → busy` for a transition that had already happened, or report a
  `prev` that was never the state it overwrote. The journal is consumed as a
  transition log and is hash-chained, so a wrong `prev` is worse than a
  redundant row. Read and write now share one transaction; the journal emit
  stays outside it.

- **Eleven redundant indexes dropped
  (`20260810120000_drop_redundant_indexes`).** Each was a leading-prefix
  duplicate of a longer index on the same table, so the planner could never
  choose it while every write still maintained it — on `missions`, `chats`,
  `assignments`, `credentials`, `journal_entries` and others.
  `idx_journal_trace_id` and `idx_journal_trace` turned out to be the same
  index under two names. A schema invariant test now fails the build if the
  shape is reintroduced.

- **One attachments table, and `chat_attachments` is gone (#1768).** The schema
  carried two attachment tables that no product code had ever read or written —
  `chat_attachments` and `workspace_files` (both migration v57) — while the chat
  composer's live attachment path wrote blobs and **no metadata row at all**, so
  a chat attachment had no recorded size, checksum, MIME type or uploader and
  could not be listed or deleted through any API. The new `attachments` table
  replaces `chat_attachments` (dropped in the same migration) and holds all owner
  kinds through an exclusive-arc foreign key, so a row can never decay into an id
  that resolves to nothing. `ProxyHandler.AgentChatAttachment` now writes its row;
  the chat blob's location is deliberately unchanged, because
  `/output/<agentSlug>/attachments/…` is the agent-visible contract.

  `workspace_files` is **not** dropped here: it is a path→metadata index rather
  than an attachments table, and three v144 timestamp-regression guards use it as
  their canary. Removing it belongs with #1768 item 8, together with moving those
  guards.

### Security

- **Go toolchain bumped 1.26.5 → 1.26.6, clearing eight advisories (#1959).**
  Seven were in the standard library and reachable from real call paths —
  `crypto/tls` post-handshake message flooding (GO-2026-6090), `net/http`
  missing `ReadHeaderTimeout` on the unencrypted HTTP/2 check (GO-2026-6089)
  and its IDNA Punycode label handling (GO-2026-5026), quadratic
  `net/url.resolvePath` (GO-2026-6218), unbounded recursion in `encoding/xml`
  (GO-2026-6088) and `encoding/asn1` (GO-2026-5972), and JavaScript regexp
  context tracking in `html/template` (GO-2026-6091). The eighth,
  GO-2026-6222, was excessive memory allocation decoding VP8L in
  `golang.org/x/image`, reachable from avatar upload, and is fixed by that
  module's 0.44.0 → 0.45.0 bump.

  Dependabot proposed the toolchain move in the `Dockerfile` alone; the
  version is pinned in **thirteen** places (`go.mod`, the `Dockerfile` and
  eleven workflow pins), and only moving them together changes what the
  released binary and CI are actually built with. `govulncheck ./...` now
  reports zero.

  The govulncheck allowlist is emptied in the same change. Its five entries
  were all `docker/docker` advisories, and that module left the graph when the
  container provider moved to `github.com/moby/moby` — a dead entry would have
  silently re-accepted those IDs had it ever returned transitively. The
  allowlist arm of the gate now has unit coverage in
  `scripts/security-yml-test.sh`, extracted verbatim from the workflow the way
  the scheduled-report gate already was.

- **Capability tokens are hashed at rest (#1888).** `port_exposures.token` and
  `pipeline_webhooks.token` were stored in the clear and looked up by equality.
  Neither `/exposed/{token}/…` nor `POST /api/v1/webhooks/{token}` has any
  authentication in front of it — by design, the token *is* the authorization
  — so anyone who could read the database file walked away with every live
  exposure URL and every configured webhook on the instance. Both columns now
  carry a SHA-256 digest and are resolved by digest, the way `cli_tokens` has
  worked since Patch J.

  **Existing tokens keep working.** The migration hashes the cleartext already
  in the row rather than rotating it: invalidating would have broken every
  published exposure URL and every already-configured sender, on every
  instance, and nothing suggests the cleartext leaked. What changes is that the
  cleartext is then overwritten — neither column can be dropped (both are
  `NOT NULL UNIQUE`, which SQLite's `DROP COLUMN` refuses, and one is still
  named by the create path) so each holds `redacted:<row id>` afterwards.

  **The digest is unkeyed SHA-256** (hex, behind an `sh1:` scheme prefix), not
  an HMAC. A key protects a digest whose input space is small enough to
  enumerate offline; both of these tokens are 32 bytes of `crypto/rand`, so the
  preimage search is 2^256 wide either way and a key buys nothing. It would
  have cost a great deal: an earlier revision of this work keyed the digest off
  `ENCRYPTION_KEY`, and the documented master-key rotation
  (`CREWSHIP_ENCRYPTION_KEY_VERSION` + `ENCRYPTION_KEY_V2` +
  `POST /admin/reencrypt`) **retires that variable as its final step** — after
  which every presented token would hash under a different scheme than every
  stored digest, with the cleartext already overwritten and nothing to recover.
  The keyed `hk1:` scheme never shipped and is not supported; a **dev instance**
  that ran the earlier revision must re-create those webhooks and re-request
  those exposures.

  A row the backfill cannot hash is no longer stranded either. The backfill
  continues past a failed `UPDATE` (SQLite has one writer; a moment of
  contention used to abort the loop and 404 every remaining webhook until the
  next restart) and, while any row still holds cleartext, the webhook lookup
  falls back to it — bounded to rows whose digest is missing, and re-hashing
  each one it resolves. On the exposure side, an `ACTIVE` row whose hashing
  failed is hashed at boot and keeps serving; one that nothing can resolve is
  flipped to `EXPIRED` instead of being reported live with a future expiry.
  Revoking an exposure now drops it from the registry by row id when the row
  carries no digest — the previous fallback read a column that always holds
  `redacted:<id>`, so a revoke could answer `200` while the capability URL kept
  reverse-proxying into the crew container until the process restarted.

  One behaviour change follows from it: a webhook's token is now **shown once**,
  in the create response. `GET`/`PATCH` and the list endpoint no longer return
  it, because it is no longer recoverable. A webhook whose token was lost has to
  be re-created, and `crewship routine webhooks url <id>` now says so and exits
  non-zero instead of printing `…/api/v1/webhooks/` with nothing after the
  slash.

  For port exposures the cleartext never reaches disk at all: the create path
  writes the spent marker and the digest directly, rather than inserting the
  live token and redacting it a moment later. That window was short — one
  request — but a crash, a WAL checkpoint or a backup taken inside it captures
  a working capability URL, which is the whole exposure being closed. If the
  process dies between the insert and the registry write, the row is left
  resolving for nobody, which is the right direction to fail.

  `pipeline_waitpoints.token` is **not** covered. It is the same kind of
  secret, but it is also that table's primary key, the handle
  `inbox_items.source_id` and a WAITING run's `waitpoint_token` carry, and the
  value `GET …/pipelines/waitpoints` reads back out of the column to rebuild
  the public callback URL. It is a retrievable shared secret by contract rather
  than a show-once credential, so hashing it means redesigning that contract
  first; tracked separately.

- **`workspace_members.role` is constrained to the roles that exist (#1893).**
  `crew_members.role` has carried a `CHECK` since v99; the column deciding what
  a member can do across a whole *workspace* carried nothing and would accept
  any string. The API validates today, so this is defense in depth — but the
  read tier is where it would have mattered: `canRole`'s write tiers switch
  over the known roles and deny anything else, while its `read` tier accepts
  any non-empty string. Write fails closed, read fails open, and only the
  schema can refuse the value independently of whichever write path appears
  next.

  Enforced with `BEFORE INSERT` / `BEFORE UPDATE` triggers rather than a
  `CHECK`, because SQLite cannot add one without a full table rebuild — not a
  trade worth making on the table that decides workspace ownership. The
  difference is deliberate: a stored legacy row keeps working and is refused
  only when something tries to write it back, where a rebuilt `CHECK` would
  have refused to apply at all and failed the boot.

- **The escalation wait endpoint now scopes to the caller's token binding.**
  `GET /api/v1/internal/escalations/{id}/wait` loaded the escalation by id
  alone, with no workspace or crew predicate — and when the escalation's type
  is `CREDENTIAL`, that handler **decrypts the resolution and returns the
  secret in the clear**. A crew-bound (`crwv1`) sidecar token could therefore
  wait on another crew's escalation, in another workspace, and be handed its
  plaintext the moment a human approved it. Both halves leaked: an
  already-`RESOLVED` foreign escalation returned immediately, and a foreign
  `PENDING` one long-polled and was delivered on resolve.

  The lookup now carries the predicate its sibling `CreateEscalation` has
  used since PR-F24: crew-bound callers match their own `crew_id` exactly (a
  sibling crew in the same workspace is as foreign as another tenant, which
  is what `crwv1` tokens exist to enforce), workspace-bound callers match
  their workspace, and the unbound master token stays unrestricted. A refusal
  is the same `404` as an unknown id rather than a `403`, so the endpoint is
  not an existence oracle.

- **The Docker API surface the server can reach is now specified, published
  and gated (#1826).** The server talks to Docker directly, which is
  root-equivalent on the host. `DOCKER_HOST` already let an operator put a
  filtering proxy in front of that, and `docker/docker-compose.prod.yml`
  already shipped one — but nobody had ever derived the endpoint list behind
  it, and it was wrong: **`COMMIT` was missing**, so crew provisioning behind
  the proxy we ship would have failed with a `403` on `POST /commit`, and the
  BuildKit path (`BUILD` + `SESSION`) was unaccounted for entirely.

  The real surface is **31 Engine API endpoints** — not the 25 previously
  assumed. `ContainerCommit`, `ContainerPause`, `ContainerUnpause`,
  `CopyFromContainer`, `ImageList`, `ImageRemove`, `Info`, `Ping` and
  `ServerVersion` were all reached and unlisted; `ContainerLogs` and
  `NetworkRemove` were listed and never called. `scripts/docker-api-surface`
  re-derives the list from the call sites on every CI run and fails on drift in
  **both** directions — a new call nobody declared (the proxy would deny it in
  production) and a declared endpoint nobody calls (we would be asking
  operators to grant a permission we do not need). It also catches a package
  newly importing the Docker SDK, and a subprocess executed with `DOCKER_HOST`
  pinned, which is a second client of the same socket that no compile-time
  check would see.

  New guide: [Docker Socket Proxy](https://docs.crewship.ai/guides/docker-socket-proxy).
  It is deliberately unflattering about the ceiling. The proxy filters URL
  paths and never request bodies, so it **cannot** block `Privileged: true` in
  a `ContainerCreate` — the workspace `allow_privileged_credentials` gate stays
  the real fence. Its granularity is a path prefix, not an endpoint:
  `CONTAINERS: 1` also opens `POST /containers/prune` and
  `POST /containers/{id}/update` for every container on the host. And the verbs
  we genuinely need (`ContainerCreate`, `ExecCreate`/`Start`/`Attach`,
  `CopyToContainer`) are most of what an attacker would want. What it does
  remove is real — image build, `commit`, swarm, plugin install, `/system`
  prune, registry auth — and that is the claim the page makes, no larger.

- **Internal tokens are now bound to a crew, closing the
  credential-metadata enumeration leak (#1159).** The `?crew_id` scope on
  `GET /api/v1/internal/credentials` (#1031) was opt-in and fail-open: a
  workspace-bound `X-Internal-Token` holder could omit `crew_id` (get the
  whole workspace's credential metadata) or forge a sibling crew's id and
  enumerate it. A per-crew sidecar now receives a **crew-bound** token —
  `crwv1.<workspace_id>.<crew_id>.<hex(HMAC-SHA256(master, ctx ||
  workspace_id || crew_id))>`, `internaltoken.DeriveCrewToken` — carrying
  the workspace binding (PR-F24) plus a crew binding. `requireInternal`
  rejects a `?crew_id` that disagrees with the token's crew (403) on every
  internal route and exposes the bound crew via
  `InternalTokenCrewFromContext`; the credential listing scopes to that
  cryptographic crew in preference to the forgeable query parameter. The
  loopback exemption for the crew-less in-process `TokenSyncer` is
  connection-based (`r.RemoteAddr`), never header-based. Crew-less runs
  fall back to the workspace-bound `wsv1` token unchanged. A follow-up
  audit pins the closure end-to-end — real derived token → `requireInternal`
  → handler, from a Docker-bridge origin — and adds a sentinel for the one
  surviving residual: a crew-less (workspace-bound) caller still gets its
  own workspace's credential metadata, because that is the token's true
  scope and because an empty listing would make the sidecar's credential
  reaper evict every provider key the container booted with.
- **Privileged crews now emit a provision-time WARN (#1032).** A crew
  provisioned with `--privileged` (e.g. DinD) collapses the UID 1001/1002
  boundary that isolates the sidecar's IPC token and injected credentials
  from the agent process. The full remediation is out of scope; the log
  line surfaces the trust downgrade in ops the moment such a crew is
  provisioned.

- **`POST /workspaces/{id}/skills/generate` spent the wrong tenant's Anthropic
  key.** The handler took its workspace from `r.PathValue("workspaceId")`, but
  `RequireWorkspace` resolves the tenant with `?workspace_id=` taking priority
  over the path — so the two could disagree. An OWNER of workspace A could send
  `POST /api/v1/workspaces/<B>/skills/generate?workspace_id=<A>`: the middleware
  validated A's membership and let the request through, then the handler
  evaluated A's role against workspace B and looked up, decrypted, and spent B's
  Anthropic API key. It now reads `WorkspaceIDFromContext`, the only value
  membership was ever checked against. A source guard
  (`TestNoHandlerReadsWorkspaceFromPath`, AST-based, allowlist with reasons)
  fails the build on any new handler that re-reads the path, so the class is
  closed rather than the one instance.

- **Six write paths persisted foreign-workspace references.** `project_id` and
  `milestone_id` on issue create/update, `project_id` on recurring-issue
  create/update, `crew_id` and `project_id` on triage-rule create/update, and
  `lead_id` on project create/update all went into their INSERT verbatim. This
  is the #1471 class on the columns that fix did not reach: the caller never
  requests the foreign row, they name it, and the read path resolves it — so an
  issue listing rendered another tenant's project name, and a triage rule could
  route this tenant's incoming issues into another tenant's crew on every match.
  `assertFKInWorkspace` now carries a per-table membership query (it previously
  supported only `agents` and `crews`, and projects/milestones were documented
  as "deliberately not listed", which is why those handlers validated nothing),
  and `fkInWorkspaceOrReject` makes a call site one line. Found by the new
  cross-workspace fence matrix rather than by review.

- **An agent could escape its crew's `autonomy_level` by creating a new crew
  (#1768).** Six `/api/v1/internal/*` routes let an agent create something that
  keeps acting after the request ends — a crew, a persistent agent, a mission,
  a cron schedule, a skill — and none of them consulted the calling crew's
  policy. Each handler justified skipping the check with a claim about its
  caller ("enforced upstream") rather than a check; nothing ran it. The sharpest
  consequence: `POST /internal/crews` never set `autonomy_level`, so the new
  crew took `DEFAULT 'guided'` (migration v101) — an agent held to `strict`
  could create a `guided` crew, create an agent inside it, and act there
  unbounded. **Two things close that, and neither is the blocking:** `strict`
  now refuses `crew_create` outright, and an allowed crew **inherits the
  creating crew's autonomy level** instead of the column default, so no created
  crew is ever more permissive than its creator. That property — not any
  particular cell of the decision matrix — is what closes the escape, and
  `TestAutonomyInvariant_ChildCrewNeverOutranksCreator` pins it across all four
  levels of the creating crew so a future re-tuning of the matrix cannot
  silently reopen it. Every arm (refused, held, allowed) writes an audit row
  carrying `decision`, `autonomy_level` and `policy_action`; there is no silent
  allow. When the policy resolver is unwired the gate holds rather than
  proceeding — a wiring bug fails closed.

### Changed

- **Agent-driven creation is now gated on the crew's `autonomy_level`, and on
  the default level (`guided`) some of it blocks (#1768).** This is a
  behaviour change on default settings, not only a bug fix — a crew that has
  never had its policy touched will see agents stopped where they previously
  were not.

  On `guided`, **`POST /internal/crews` and `POST /internal/agents` are
  blocking**. The row is still written, but inert: a new crew is pinned to
  `autonomy_level=strict` (so nothing can be created inside it) and a new agent
  is written `status=PENDING_REVIEW` (so the chat bridge will not start it).
  The call answers `202 Accepted` with `pending_review: true` and an
  `approval_id`. The operator gets a **blocking, ADMIN-addressed inbox
  waitpoint** and a row on the approvals queue.

  Release a held item with `crewship approvals approve <id>` (or deny it, or
  the `/approvals` page — `POST /api/v1/approvals/{id}/decide`, OWNER/ADMIN).
  Approving flips the sentinel: the agent becomes `IDLE`, the crew is restored
  to **the creating crew's** level — never higher. **Denying does not delete
  anything**, it just never releases, and so does letting the hold lapse: the
  seven-day timeout leaves the artefact inert rather than turning into a green
  light.

  On `guided`, **missions and cron schedules are not blocking**. They proceed
  and leave a non-blocking inbox notice: a mission creates no principal and is
  pinned to the caller's own crew, and a schedule stays an operator-editable
  row (`PATCH .../pipeline-schedules/{scheduleId}`), so in both cases a hold
  bought the same visibility a notice does while stopping ordinary work. At
  `strict` a mission is held (it can be planned, not started) and a schedule is
  refused outright.

  To tighten or loosen this per crew:

  ```bash
  crewship policy set --crew <slug> --level strict  --reason "…"  # refuse crew/agent/schedule creation
  crewship policy set --crew <slug> --level trusted --reason "…"  # unchanged for crew/agent; missions + schedules go journal-only
  crewship policy set --crew <slug> --level full    --reason "…"  # nothing blocks; crew/agent still leave a notice
  ```

  `strict` refuses `crew_create`, `agent_create` and `routine_schedule_create`
  with a structured `403` naming the level, so the CLI can suggest the
  `policy set` that would unblock it. `full` blocks nothing but still leaves a
  non-blocking notice for a new crew or agent — full autonomy is still
  autonomous, but a new permanent principal is the change an operator wants to
  see having happened.

- **Agent-created missions are now bounded by two live instance settings
  (#1768).** Letting `guided` — the default level — start a mission without a
  hold rested on missions being bounded by the #1757 delegation caps. They are
  not: the mission engine dispatches its task list through a path that never
  passes the delegation insert, and both files say so. `POST /mission/create`
  now answers `403` past either of:

  | Setting | Default | Bounds |
  |---|---|---|
  | `mission.max_tasks` | `16` | Tasks one agent-created mission may carry. `0` allows task-less missions only. |
  | `mission.max_active_per_crew` | `6` | Agent-created missions one crew may have in a non-terminal state. `0` switches agent-driven mission creation off. |

  The refusal body names the `setting` an operator would change, and no mission
  or task row is written. Both are read live from `app_settings`, so
  `crewship instance settings set mission.max_tasks 24` applies to the next
  call, not the next restart, and neither number can be raised from the request
  — there is no such field on the route.

  The second number is the one that matters, because **mission creation
  recurses**: a mission created with no tasks makes the engine run its lead as
  a planning turn, that turn is the one dispatch shape that keeps the sidecar,
  and the planning brief we send it offers "create a new sub-mission" as an
  option. A per-mission task cap alone would have bounded nothing. The crew's
  live-mission budget does, because every mission an agent creates lands in the
  crew its token is bound to.

  Scope, stated rather than implied: this covers the **agent** door only.
  Missions created through the dashboard/JWT API are neither capped nor counted
  against the agents' budget — an operator planning work is making a decision —
  and issues (which share the `missions` table) never count.

### Fixed

- **The status chips on `/issues` could only ever select one status.** The
  chip row was handed the list `useFilteredIssues` had *already* narrowed by
  status, so picking "Backlog" dropped every other chip's count to 0 — and a
  chip whose count is 0 is not rendered at all, which put the second status
  permanently out of reach. The "All" pill had the mirror-image bug: it
  advertised the filtered count as the total. The hook now returns
  `{ visible, statusFacet }` — `visible` is what the board and list render,
  `statusFacet` is the same set with the status filter left out, and it is
  what the chips count. The code comment above the call site had described
  the correct behaviour ("counts derive from the pre-status-filter set")
  since it was written; only the code disagreed.

- **Bulk editing from the issues list view did nothing.** `IssuesListInline`
  — the wrapper `/issues` actually renders in list mode — never forwarded
  `workspaceId`, so `IssuesListView.handleBulkUpdate` returned at
  `if (!workspaceId) return`: select rows, pick a status, no request, no
  error, no feedback. That also made the refusal reporting added in #1563
  unreachable from the real UI. The wrapper now requires `workspaceId` (and
  forwards `onBulkAction`), and the regression is pinned at the wrapper
  level rather than only on the inner view, which the old tests had been
  handing the prop themselves.

- **Renaming a label was a 500 for everyone.** `PATCH /api/v1/labels/{labelId}`
  built its statement with `newUpdate()`, which always emits `updated_at = ?`
  first; the `labels` table has only `created_at`, so SQLite answered "no such
  column: updated_at" on every call since the endpoint shipped. The statement is
  now built from the columns the table actually has. Worth noting how it
  surfaced: a route that 500s cannot be tested for tenancy — the UPDATE never
  executed, so nothing could be said about its `WHERE` clause — and fixing the
  500 is what made the fence assertion on that route real.

- **A local-model endpoint now works whatever shape it was stored in.** One
  `ENDPOINT_URL` credential was consumed by three code paths that each expected a
  *different* shape of the same string: a bare root for `llm.Ollama` (which
  appended `/api/chat`), `.../v1` for the OpenCode provider block, and the full
  `.../v1/chat/completions` for `llm.OpenAI` (which used the value verbatim as the
  POST target). Our own documentation tells operators to store the `.../v1` form —
  point the Keeper governance model at that credential and the judge POSTed to
  `.../v1/api/chat`, got a 404, and, Keeper being fail-closed, **denied every
  credential request**. The credential's own Test button stayed green throughout,
  because the reachability probe strips `/v1` before falling back to `/api/tags`.
  A new `internal/llm/endpoint` package normalizes any pasted shape — bare
  `host:port`, trailing slash, `/v1`, `/v1/chat/completions`, `/api/chat` — to a
  mount root, and each provider appends the path for the wire it speaks, so the
  mismatch is now unreachable by construction. A reverse-proxy mount prefix
  (`https://gw/ollama`) and an Azure-style unversioned deployment are both
  preserved, as is an `?api-version=` query.

- **A reasoning model no longer silently denies everything as the Keeper judge.**
  Ollama returns a reasoning model's chain of thought in `message.thinking`,
  separately from `message.content`. A model that spends its token budget
  thinking (verified against Ollama 0.32.5 with `qwen3:4b`) answers with empty
  content, `done_reason: "length"`, and HTTP 200 — which the provider reported as
  a successful, empty, `end_turn` completion, so the fail-closed judge parsed
  nothing and denied, with no error to explain it. `Response` now carries
  `Thinking`, and a budget-truncated answer reports `max_tokens` instead of
  `end_turn`, so callers can tell "the model said nothing" from "the model
  reasoned and never reached an answer".

## [1.0.0-rc.1] — 2026-07-12

### Security

- **X-Internal-Token is now bound to a workspace (PR-F24) — closes the
  documented symmetric cross-tenant bypass.** Sidecars no longer
  receive the process-wide master internal token. At sidecar start the
  orchestrator derives a workspace-bound token
  (`wsv1.<workspace_id>.<HMAC-SHA256(master, workspace_id)>`,
  `internal/auth/internaltoken`) and injects it via the stdin
  `IPCConfig`. The `internalAuth` middleware validates the binding on
  every `/api/v1/internal/*` request: tampered tokens fail the
  constant-time MAC check, and the binding is enforced as a **mandatory
  request scope** rather than an optional `?workspace_id` check. For a
  bound token `requireInternal` rejects a `?workspace_id` that disagrees
  with the binding (403) and **injects** the bound workspace when the
  caller omits the query — so every handler that filters by
  `?workspace_id` (webhook secret, list credentials, agent/chat resolve,
  crew/agent create) is tenant-scoped automatically, with no
  legacy-unscoped fall-through. Path-param mutations that don't read the
  query (chat message-count / title, run finalize, credential status)
  constrain their `WHERE` clause by the bound workspace (foreign rows →
  404, never mutated). Handlers scoped by a body-carried `workspace_id`
  (`cost/record`, `journal/emit`, `pipelines/save`, plus the issue /
  mission / assignment / query / escalation create handlers and the
  confidence report) enforce the same binding in-handler via
  `assertInternalTokenWorkspace`. The unbound master token — which
  authorizes every workspace and so retains cross-tenant power if leaked
  from a container — is now **pinned to a loopback origin**: a master
  token presented from a Docker-bridge / LAN IP is refused (403), capping
  the blast radius of a leaked master to the host trust boundary
  (`CREWSHIP_INTERNAL_ALLOW_ANY=true` relaxes the pin for reverse-proxy
  setups). Pre-fix, an agent that captured the token inside its container
  could aim internal routes at ANY workspace by picking the
  `?workspace_id` it wanted (or simply omitting it on the routes that
  ran unscoped) — the "symmetric case" left open by the earlier Keeper
  Phase 2 asymmetric fix below. Derivation is stateless (no persistence;
  derived tokens roll with the master each boot) and the master token
  remains valid for host-side trusted callers (chat bridge, webhook
  secret resolver, LLM proxy) that reach the API over loopback and never
  enter a container. See the updated "Tenant isolation" section in
  `docs/security/threat-model.mdx` and the retired "known exception"
  block in `docs/api-reference/internal.mdx`.

- **Cross-tenant scoping on Keeper Phase 2 internal endpoints.** The four
  `/api/v1/internal/keeper/*` handlers (skill-review, behavior,
  memory-health, negative-learning) now (a) include `workspace_id` in
  the `self_learning_enabled` lookup WHERE clause and (b) reject any
  request where the body `workspace_id` disagrees with the request
  context `workspace_id` set by `internalWsCtx`. Pre-fix an internal-
  auth caller could pass an `agent_id` from workspace A while claiming
  workspace B in the body and read the gate flag — asymmetric cross-
  tenant bypass. The symmetric case (caller picks one workspace
  consistently across query + body) is closed by the workspace-bound
  `X-Internal-Token` entry above (PR-F24); the former "known
  exception" block in `docs/api-reference/internal.mdx` is retired
  accordingly.

- **Lessons memory tier hardened against agent-author writes.** The
  generic `memory.write(tier="lessons", …)` dispatcher path returned
  cap=0 and bypassed every governance layer the F4.4 evaluator path
  enforces (schema validation, idempotency by ID, atomic-rename,
  flock). Agent-author writes to the lessons tier are now rejected
  with an error that points at the F4.4 negative-learning endpoint as
  the supported entry point. Tombstone test
  `TestDispatch_Write_LessonsTier_Rejected` pins the contract.

- **Security hardening v1 wave.** Two-tier CLI tokens (standard +
  HMAC-keyed admin) with fine-grained PAT-style scopes, per-crew role
  overrides and per-agent ownership gates, structured 403s with a denial
  audit log, one-shot setup token on `/api/v1/bootstrap`, internal API
  refuses non-private client addresses, sidecar credential proxy strips
  plaintext token fields, `NET_RAW` dropped from default crew container
  capabilities, `/crew/init.sh` auto-exec now opt-in per crew, and
  bootstrap/pair tokens widened to 256-bit (#462).

- **May–June external-audit remediation waves.** Prod `docker.sock` is
  brokered through a filtering proxy (#478); memory writes are scanned
  *before* persist, including invisible-format (Cf) codepoint evasion
  (#502, #477); a TOCTOU symlink window in `writeCredentialFiles` closed
  (#464); path traversal, IDOR, cross-crew access, MCP SSRF,
  prompt-injection and info-leak findings fixed across four passes
  (#476–#508, #612, #619, #620, #651, #733, #752).

- **RBAC chokepoint: route-table declared authz.** Previously
  un-gated control-plane mutation endpoints now enforce role checks
  (#792); every route's permission gate is declared in the route table
  and pinned by an invariant test so new endpoints can't ship ungated
  (#824, closes #809/#811); admin console + system endpoints get a
  uniform ADMIN+ floor (#893); scoped CLI-token permissions are enforced
  at the same route-table chokepoint (#888).

- **Ingress trust fence.** Untrusted external content (webhook bodies,
  poll payloads) is neutralized before it can enter an agent prompt
  (#819, #808 M0), extended to the remaining mission/task and
  crew-context ingress sites (#918); `tool_result` events from **all**
  adapters now pass through one shared injection-scan chokepoint (#950).

- **Per-agent sidecar identity.** Each agent in a crew receives its own
  derived auth token, closing intra-crew identity spoofing against the
  sidecar control plane (#826, closes #812), and escalations are
  attributed to the bound agent rather than a caller-supplied `from`
  field (#796). Per-agent memory identity fixes landed in the dev2
  validation sweep (#779, CRE-137…146).

- **Credential revocation is now effective at runtime.** The sidecar's
  in-memory credstore reaps revoked credentials within ~60s (#795), and
  revoking a credential also removes its file-based `/secrets` materials
  from running containers (#903, closes #814).

- **Webhook trigger hardening.** Every pipeline webhook dispatch
  requires an HMAC signature (#501) with replay auto-dedupe (#506) and a
  rate-limit floor (#507); empty-secret HMAC configs are rejected and
  optional signed-timestamp replay defense added (#789), with per-agent
  `require_timestamp` to close the body-only replay window (#822,
  closes #815).

- **New crews default to restricted egress**, and egress-policy denials
  are loud instead of silent (#793); `http` pipeline steps enforce the
  crew egress policy and resolve credentials from the vault rather than
  inline secrets (#778).

- **Memory file-path hardening.** Centralized pathsafe fencing on memory
  paths plus capped search allocations close 13 CodeQL HIGH findings
  (#926); memory-read and writer-lock opens use `O_NOFOLLOW`, closing a
  symlink TOCTOU (#936, closes #934/#935).

- **Log-injection neutralized centrally.** One CR/LF + control-character
  neutralizer covers server and sidecar log sinks (#938); secret
  redaction is wired through slog (#488) and raw token values in logs
  replaced by fingerprints (#486).

- **deb/rpm packages are GPG-signed** and the public key published for
  apt/dnf verification (#942, closes #932).

- **Go toolchain bumped 1.26.0 → 1.26.5** across the release cycle for
  stdlib CVEs, including the crypto/tls GO-2026-5856 advisory
  (#570, #907).

### Added

- **Pipeline resume-from-step at boot.** The executor now persists
  `current_step_id` + the step-outputs map at every step boundary, and
  boot recovery re-enters previously in-flight runs from the next
  unfinished step instead of stamping them `interrupted`: completed
  steps are restored (not re-executed), the in-flight step re-runs
  with at-least-once semantics, DAG runs recover at wave granularity,
  and runs parked on a `wait` approval step re-attach to their
  original pending waitpoint token so the approval card stays
  answerable across restarts. `interrupted` remains the fallback when
  persisted state is insufficient (missing pipeline, definition drift,
  non-resumable mode), with the reason recorded in `error_message`.
  Operator escape hatch: `CREWSHIP_PIPELINE_RESUME=off` restores the
  old stamp-everything-interrupted behaviour. See
  `docs/guides/routines.mdx` § Durability and restart recovery.

  Hardening pass on the resume scan: (1) the boot scan now runs
  BEFORE the cron scheduler starts and is additionally fenced on
  process-boot time + the live run registry, so a scheduled run fired
  at boot can never be "resumed" into a second concurrent execution
  under the same run id — `RunRegistry.Acquire` also rejects
  duplicate run ids outright instead of silently overwriting the live
  entry; (2) a resumed run that loses the concurrency-slot race
  retries with capped exponential backoff instead of being
  permanently stamped interrupted; (3) migration v114 stamps the
  pipeline's definition content hash onto each run row at start, and
  resume refuses (→ `interrupted: definition changed`) when the
  definition was edited in place even if every step id survived;
  (4) a waitpoint that timed out during downtime now fails the
  resumed run with `timed out` instead of misreporting `denied`.

- **Domain metrics on `/metrics` (W10).** The Prometheus endpoint now
  exposes operator-facing series next to the existing process gauges:
  `crewshipd_assignments{status}`, `crewshipd_assignment_queue_depth`
  / `_queue_crews` / `_queue_depth_max` (aggregated — no per-crew
  labels by design), `crewshipd_pipeline_runs{status}`,
  `crewshipd_agent_run_events_total{event}`,
  `crewshipd_llm_calls_total{provider}` +
  `crewshipd_llm_cost_usd_total{provider}` from the paymaster ledger,
  `crewshipd_containers_tracked` / `_reporting`, and
  `crewshipd_db_migration_version`. Label sets are closed (unknown
  values fold into `other`), the DB-derived block is cached for 15s,
  and migration v113 adds the two status indexes the counts ride on.
  Documented in `docs/observability/metrics.md`.

- **PR-G / PR-F UI surface.** Three React panels expose previously
  backend-only governance toggles: `CrewPolicyControls`
  (`autonomy_level` × `behavior_mode` × `max_ephemeral_agents`),
  `AgentLearningToggle` (per-agent `self_learning_enabled` flag from
  migration v106), `AuxStatusSection` (read-only diagnostic of the
  five auxiliary model slots). Plus four keeper P2 review queue
  sub-tabs in the admin panel, a GDPR admin export/delete panel,
  inbox approve-hire button, and codemirror markdown editor for the
  agent memory tab.

- **Migration v106 — per-agent `self_learning_enabled` flag** with the
  standard audit triple. Consumed by the F4.4 negative-learning and
  F6 persona-suggest ALLOW paths: when the flag is OFF, the proposal
  queues a blocking inbox row with the full proposal payload instead
  of auto-applying.

- **Migration v107 — GDPR cascade primitives.** `data_subject_id`
  columns on `memory_versions` and `inbox_items`, plus the
  `gdpr_actions` audit table. New `DELETE /api/v1/admin/users/{id}/data`
  cascade endpoint + `GET` Art. 15 export bundle. Idempotent — each
  invocation lands a new `gdpr_actions` row.

- **`MemoryProvider` interface and `LocalDispatcher` reference impl**
  for future pluggable memory backends. Additive — existing production
  call sites still route through the built-in dispatcher; swap lands
  as PR-F17.

- **`AgentBrief` sub-agent briefing primitive.** Replaces the
  all-or-nothing `SkipConvHistory` boolean with a curated `Mission` +
  `SharedMemory` + `Constraints` slice written to the child's
  `BRIEF.md`; picked up by the orchestrator's prompt assembly.

- **Declarative deployment manifests (SPEC-2).** `crewship apply` covers
  14 manifest kinds end-to-end with validation (duplicate-slug detection
  at validate time), plus auto-managed sidecar credentials (SPEC-4)
  (#454, #456, #587).

- **Routines platform wave.** Governance review gate
  (proposed → approve, with OWNER/ADMIN `skip_governance_gate`),
  describe-first authoring from chat, `save_routine`/`list_routines` as
  native MCP tools, precondition gates and declared required
  integrations, CEL expressions, deferred dispatch, hooks and input
  streams (#743, #715, #739, #755). Follow-on: first-class token-zero
  `script` steps (#849) with portable export/import bundles (#913),
  per-step retry policy with backoff + CEL classifier (#882),
  auto-parallelized independent steps with bounded DAG waves (#872),
  single-step fixture runs (#854), `routine init` scaffold/clone (#860),
  run-result retrieval (#844), run→files listing (#891), client-facing
  shareable/redacted progress views (#877), offline `validate` parity
  with server-side checks (#901), and non-blocking `notify` steps with
  outbound email + signed-webhook channels and anti-spam caps
  (#843, #859, #910).

- **Managed integrations via Composio.** Catalog, OAuth connect flow,
  per-agent binding, tool exposure and triggers (#696), a flag-gated
  default connector for all agents (#699), portal-safe Add-app UX
  (#703), and agents are made aware of their connected integrations
  (#704).

- **Chat overhaul.** Faithful history with reload, grouped
  tool/reasoning UI, sub-agents and multi-user group chat (#702);
  resumable streams so a reply is never lost to a refresh (#757); smooth
  streaming reveal (#758); unread badges, activity ordering and
  agent-replied notifications (#760); mid-turn steering of an in-flight
  run, long-conversation compaction instead of truncation, cross-session
  conversation search (BM25), and LLM model discovery with live model
  switch (#630 wave).

- **Fleet operations UI.** The Journal Runs tab is reworked into a fleet
  ops overview (#750), a global Activity Bar + readable run-activity
  timeline (#701), context-aware bottom dock across Issues, Routines &
  Activity (#710), and a drillable agent-native run trace — tool-call
  sub-spans, waterfall, persisted per-step input/output with lazy fetch
  and syntax highlighting (#852, #853, #874), plus per-step
  container-ready timing (#930).

- **Inbox redesign.** Gmail-style triage with formatted detail,
  noise/secret hardening and CLI parity (#708), collapsible group tree
  with bulk select/resolve (#692), refined toolbar (#747), and
  agent-proposed credentials with one-click human approval (#706).

- **Ephemeral ("hired") agents surfaced in Crews & Agents** with full
  lifecycle controls (#693); hired agents automatically receive an
  Anthropic credential so they can actually run (#680).

- **Slash commands + per-user capabilities.** Server-driven slash
  palette with CLI/UI parity, gated by per-membership capability grants
  (#595).

- **Packaging: deb/rpm packages + systemd unit** for server installs
  (#927, #858 phase 4), and **Windows support via a CLI-only
  `crewship-cli` build** — zip archives, `O_NOFOLLOW` platform split,
  gated self-update (#949, closes #945).

- **`crewship self-update`** — one upgrade command per install channel
  (brew/deb/rpm/tarball) (#915), including systemd server-mode upgrade
  orchestration with health-checked restart (#929, #858 phase 5).

- **Downgrade/version safety.** A version-skew guard refuses to boot a
  binary over a newer DB schema (#912), and
  `crewship db restore-snapshot` provides the matching downgrade
  recovery path (#924); backups got a disaster-recovery rewrite —
  `--replace` mode, schema-driven FK discovery, user reconciliation
  (#594).

- **Local-model support.** Ollama-style local endpoints for the OpenCode
  adapter with BYOK egress (#951), and a first-class `ENDPOINT_URL`
  credential type so local model endpoints live in the vault like any
  other credential (#957, closes #955).

- **Devcontainer-based provisioning.** BuildKit feature-image
  provisioning (#675), proactive auto-provision so crews are runnable on
  first dispatch (#731), deduped feature catalog preferring canonical
  publishers (#732), and persisted BuildKit stderr tails so failed
  builds are debuggable (#884).

- **Agent-authored skills.** Agents can draft skills that route through
  inbox review into a GENERATED catalog (#734).

- **CLI: agent-ready contract.** Structured errors, stable exit codes,
  `--wait`, env-var auth and API parity for driving Crewship from
  agents (#782); native server profiles for multi-instance targeting
  (#737); unified `crewship nuke` subcommands with full workspace
  teardown (#748); confirmation prompts + `--yes` on six destructive
  commands (#579); `--max-turns` on run/ask + agent schedule flags
  (#753); `routine logs --show-outputs` for post-hoc step debugging
  (#828); `me preferences` + privacy commands (#754).

- **Cost controls.** Cache-stable prompt assembly, per-run turn caps
  with a loop guard, and cheap-model routing for aux/sub-agent calls
  (#751).

- **Operator observability.** Opt-in pprof + Pyroscope push profiling
  (#552), runtime log-level toggle and disk-health reporting (#784), and
  every run surfaces the model it actually resolved to.

- **Crew memory from mission outcomes (F4.5).** Mission results are
  distilled into crew memory with provenance (#546); an evolving
  per-operator user model personalizes agent behavior over time
  (#630 wave).

- **Settings & workspace management.** Real workspace switcher (list,
  select, persist, create) (#597), editable profile with password change
  and workspace member role management (#883), avatar upload (#900).

- **Release engineering.** The release smoke pipeline runs again on a
  real trigger plus a pre-merge package smoke (#941, closes #933), and
  nightlies publish per-run immutable pre-releases instead of a rolling
  tag (#895).

### Changed

- **Frontend RBAC mirrors backend.** `AgentLearningToggle` derives
  `canEdit` from `abilities.can("manage", "Agent")` (matching the
  backend PATCH permission gate); `CrewPolicyControls` mirrors via
  `abilities.can("update", "Crew")`. Update-only users see the
  toggle disabled instead of hitting 403 at save time.

- **`Promise.all` → `Promise.allSettled`** in `CrewPolicyControls.load`
  so a quota-fetch network error doesn't poison the required policy
  fetch.

- **One request-builder for every dispatch path.** Chat, missions,
  routines and peer delegation now build agent requests through a single
  shared factory, which also activated the previously dead HITL approval
  path and fixed mission/peer MCP + prompt divergence (#825, closes
  #810); pipeline executor wiring was likewise unified behind one
  factory (#773).

- **Telemetry defaults to opt-in on stable releases** with an explicit
  onboarding consent step (crash-reporting opt-out remains for
  pre-release channels) (#645).

- **First-run bootstrap UX.** The setup-token gate is replaced by an
  n8n-style first-run flow (#593), and the bootstrap window now stays
  open until the first admin exists, with the finite time window as an
  opt-in (#785).

- **UI chrome unified for 1.0** — consistent sub-bars, sidebars and
  toolbars across pages (#749); agent access consolidated under a single
  Skills & Tools surface (#698) with a curated built-in tool profile per
  adapter (dead harness tools removed) (#705).

- **Routine `test_run` is gone** — `dry_run` returns an honest execution
  plan plus the declared capability manifest instead of pretending to
  execute (#743 wave).

- **First-party MCP tools are eager-loaded**, removing the per-run
  ToolSearch discovery tax (#745).

- **Dispatch/runtime performance.** Deferred-run dispatch is async,
  removing the one-run-per-run-duration cliff (#857); warm crew hits
  skip the host-wide Docker `ContainerList` (#876); crew containers
  prewarm on claim, off the critical path (#902).

### Fixed

- **`crewship seed` now bootstraps against the selected `--profile`, not
  `CREWSHIP_SERVER`.** The seed flow's unauthenticated bootstrap POST (and
  the `--nuke` confirmation, smoke test, and backup warmup) resolved their
  target with `ResolveServer`, whose precedence is `--server` >
  `CREWSHIP_SERVER` > config. Every *authenticated* call in the same command
  uses `EffectiveServer`, under which an explicit `--profile` /
  `CREWSHIP_PROFILE` wins over `CREWSHIP_SERVER`. So in a shell that exports
  `CREWSHIP_SERVER` (the documented multi-clone convention), `crewship seed
  --profile prod` sent bootstrap to `CREWSHIP_SERVER` while everything else
  went to the profile server — silently splitting one seed across two
  instances and surfacing as a bogus `DB already initialized` when
  `CREWSHIP_SERVER` pointed at an already-bootstrapped instance. The seed
  flow now routes every call through one `seedTargetServer()` helper backed
  by `EffectiveServer`.

- **Episodic indexer now starts at server boot.** The journal-embedding
  sweeper (`episodic.NewIndexer`) was fully implemented and tested but
  never constructed in production, so `HybridRecall` always queried an
  empty vector index. The server now starts the sweeper at boot when an
  embedder is configured (`KEEPER_OLLAMA_URL`). Without one, episodic
  recall runs in **sparse-only mode** and says so: a WARN at boot, an
  `episodic: vector|sparse-only` field on `GET /healthz`, and a
  matching `crewship doctor` check with the enable hint.

- **Stuck-QUEUED assignment sweeper now runs in production.** The
  crash-recovery sweeper (`StartStuckQueueSweeper`) was implemented
  and tested but never started — QUEUED assignments stranded by a
  crash between "row set QUEUED" and the next completion-path pump
  stayed queued forever after a restart. Server boot now starts the
  sweeper alongside the other background loops (scan every 60s, rows
  count as stuck after 10min queued; goroutine exits cleanly on
  shutdown) and logs a `stuck-queue sweeper started` line at boot.

- **Inbox `fetch()` network-error handling.** Both `wrap("approved")`
  (approve-hire) and `wrap("retried")` (routine retry) in
  `inbox-list.tsx` now wrap `await fetch(…)` in `try`/`catch`. Pre-
  fix, an offline / DNS / CORS preflight failure cleared the busy
  state with no user toast (silent success).

- **Free-form `reason` text scrubbed from app logs.** The
  `agent_learning` PATCH handler logged the operator-entered reason
  verbatim; switched to `reason_len` so centralized logs no longer
  collect PII / business context that's already on the DB audit row.

- **Inbox enqueue failures surface as 500.** The `self_learning=OFF`
  gate path in both the F4.4 negative-learning handler and the F6
  persona-suggest handler was swallowing `inbox.Insert` errors,
  returning 200 with neither lesson nor inbox row.

- **CI build break from a leaked merge-conflict marker** in
  `internal/api/agent_config.go` (PR-D `agent.status` + PR-E
  `system_prompt_legacy` collision during parallel-agent push churn).

- **Credential escalation no longer fakes success.** When a credential
  proposal can't be staged, the agent gets an error instead of a
  silent false-success (#787).

- **Recurring issues actually fire.** The dispatcher was never wired
  into server boot (#791); follow-up adds creator attribution, durable
  fire idempotency and UTC consistency (#823), and issue creators are
  recorded and displayed everywhere (#774).

- **Schedulers are at-most-once.** Cron and deferred pipeline fires
  dedupe via idempotency keys (#788), the agent scheduler gets the same
  guarantee (#820), and scheduled fires honor `target_pipeline_version`
  (#777).

- **Container-start failures are classified** instead of collapsing into
  one masked generic error, so operators see the real cause (bad image,
  legacy volume, credential prep, …) (#790).

- **Crash recovery for assignments and chats.** RUNNING assignments
  orphaned by a crash are recovered at boot (#768), orchestration loops
  re-attach to IN_PROGRESS missions (#641), webhook-triggered runs
  dispatch asynchronously instead of blocking the receiver (#769), one
  active run per chat is enforced regardless of sender (#765), and a
  chat turn never ends in silence — zero-output and restart-interrupted
  replies are surfaced (#770).

- **Memory writes are durable and fail closed.** MCP memory-write
  failures no longer report success; memory tools are only advertised
  when the backing store is healthy; restart-agents matches the right
  container (#786).

- **dev2 validation sweep (CRE-137…146).** Memory identity, keeper
  workspace scoping, webhook HMAC, cost attribution and CLI
  parity/fidelity fixes from a full-instance validation pass (#779).

- **Keeper F4.1 skill-review sweep bills a real workspace** instead of
  attributing its LLM spend to a phantom one (#970, CRE-138).

- **Full archives ship a Linux ELF sidecar on every platform.**
  Darwin-built archives previously bundled a Mach-O sidecar that could
  never run inside Linux crew containers (#968, closes #953); archives
  now include `crewship-sidecar` + `entrypoint.sh` at all (#914), and
  sidecar remediation hints are install-channel-aware with the Homebrew
  libexec layout supported (#925).

- **HTTPS login worked around a CSRF cookie-name mismatch** — the CSRF
  cookie is re-sent under the server's real `__Host-` name (#875).

- **Crew shared files reach `/crew/shared`.** Bundled `files:` never
  arrived for agentless crews (#870); delivery is container-routed with
  overwrite semantics and works for standalone SPEC-2 Crews (#928);
  re-applying an unchanged file no-ops and succeeds on a stopped crew
  (#940).

- **OpenCode adapter production parity** — usage keys, model surfacing,
  JSONL schema and EOF resilience (#948).

- **Raw internal errors no longer leak to chat/comments**; hook warnings
  surface on the run instead (#771); journal poison entries are dropped
  rather than retried forever, and dev logs are capped (#783); chat WS
  sends are guarded against the server's 64 KiB frame cap (#764).

- **Scheduler resolves agent config via loopback** instead of the
  public Next.js URL, fixing scheduled routines behind proxies (#709);
  `crewship system keeper` injects the workspace so it stops 400ing
  after the admin-floor change (#909).

- **Pre-C1 legacy Docker resources are auto-migrated** on use and an
  ops command detects + prunes orphaned pre-C1 crew resources, fixing
  the "failed to start agent container" wall after old seeds
  (#736, #738).

## [0.1.0-beta.4] — 2026-05-19

**Routines 2026, declarative manifests, security hardening.** Substantial
beta covering observability (OTel spans, prompt-cache token plumbing),
ADLC phase-7 signal (typed feedback API + thumbs UI), continuous online
grading (sampler worker), per-routine guardrails, declarative workspace
manifests with sidecar services, and security CI cleanup. v0.1.0-beta.3
was skipped — this tag bundles everything from beta.2 → beta.4 on `main`.

### Operator upgrade notes

- **Backup `crewship.db` before upgrading.** Migration v97 recreates
  the `eval_runs` table via the standard SQLite RENAME → CREATE →
  `INSERT...SELECT` → DROP pattern to widen the `kind` CHECK constraint
  for the new `online` sampling kind. The migration runs in a
  transaction so a mid-migration crash atomically rolls back, but it
  has not been benchmarked on a production-sized eval suite — schedule
  the upgrade during a quiet window.
- **`CREWSHIP_ALLOWED_ORIGINS`** must be set in production env config
  for browser-driven POSTs (Next.js → daemon cross-port). `dev.sh` now
  emits it automatically alongside other managed keys; systemd-driven
  prod deploys must add it to their unit env file.
- **Online eval sampler runs on every server boot** with a 60-second
  tick. Routines without `eval.online.sample_rate > 0` are zero-cost
  deterministic skips. Operators introducing `sample_rate: 1.0` on
  high-throughput routines should size their grader budget; the sampler
  enqueues at the routine's rate but the grader cost is per-eval.
- **Shadow features available but require operator config:**
  - **Prompt caching:** ledger + telemetry plumb provider-reported
    `cached_input_tokens` once an `API_KEY`-typed Anthropic credential
    is provisioned (Claude Code CLI tokens don't go through this path).
  - **OTel routine spans:** `routine.run` / `routine.step` /
    `agent.invoke` / `llm.call` spans emit when `OTEL_EXPORTER_OTLP_ENDPOINT`
    is set; collector wire-up is operator's choice — any OTel-compatible
    backend consumes the GenAI semconv format natively.
  - **Per-routine input-guard action policy:** DSL
    `guardrails.input.prompt_injection.action: block | sanitize | log`
    only fires for routines that opt in.

### Added — Observability

- **OpenTelemetry GenAI spans** wired across the hot path:
  `routine.run`, `routine.step`, `agent.invoke`, `llm.call` with the
  prescribed `gen_ai.*` + `crewship.*` attributes. New
  `StartRoutineRunSpan` + `StartRoutineStepSpan` helpers
  (`internal/telemetry/spans_routine.go`). Trace tree mirrors DSL
  composition; `call_pipeline` nests as a child step. Panic recovery
  pattern preserves the original crash stack across nested defers
  via `telemetry.PanicWithStack` so post-mortem traces point at the
  real explode site, not at the re-panic line. (#447)
- **Prompt-cache token plumbing** through provider → ledger → OTel.
  Anthropic's `cache_read_input_tokens` + `cache_creation_input_tokens`
  and OpenAI's `prompt_tokens_details.cached_tokens` now surface on
  `llm.Response`, flow into `paymaster.CallResponse.CachedInputTokens`,
  land in `cost_ledger.cached_input_tokens` / `cache_creation_tokens`,
  and stamp `gen_ai.usage.cached_input_tokens` on every LLM span.
  Anthropic tools array gets a `cache_control: ephemeral` breakpoint
  by default — the single highest-leverage cache hit for agent
  workloads. (#447)

### Added — Feedback (ADLC phase-7)

- **Typed per-message feedback API** (`/api/v1/feedback`) with six
  signals (helpful, not_helpful, inaccurate, unsafe, edit, regenerate)
  bound to `trace_id` for eval-mining correlation. Migration v96
  introduces `message_feedback`. POST is UPSERT-idempotent; DELETE is
  idempotent (204 on missing row); GET is workspace + per-user scoped.
  Body capped at 16 KiB via `MaxBytesReader` before JSON parse;
  per-field caps at 4096 chars on `reason` and 256 chars on id fields. (#447)
- **Frontend optimistic-update store** (`stores/feedback-store.ts`)
  with per-(turn, signal) Promise-chained serialization so a fast
  thumb-toggle can't race between POST and DELETE. State is namespaced
  by `user.id`; switching accounts on the same browser clears the
  previous user's votes. (#447)
- **Trace_id WS propagation** — `internal/chatbridge/bridge.go` stamps
  the active OTel trace id onto the `done` event metadata;
  `hooks/use-chat.ts` lifts it onto `ChatTurn.metadata.trace_id`. The
  feedback POST flows it through so every signal lands indexed against
  the routine run that produced the message. (#450)

### Added — Online eval sampler

- **Continuous production grading** via `internal/quartermaster/online_sampler.go`.
  Worker scans completed `pipeline_runs` every 60s, picks rows at the
  routine's configured `eval.online.sample_rate`, and enqueues a
  `kind='online'` eval row. Schema-layer idempotency via partial
  `UNIQUE INDEX uq_eval_runs_online_pipeline_run`; (ended_at, id) tuple
  cursor handles sub-millisecond pipeline_run completions without
  orphaning siblings; doubling-skip backoff on entropy outages capped
  at 10 ticks. Wired into `cmd/crewship` server start. (#447, #449)

### Added — Guardrails

- **Per-routine input-guard action policy**
  (`guardrails.input.prompt_injection.action`) with `block` (default) /
  `sanitize` / `log` modes. Sanitize uses offset-based replacement via
  new `Finding.MatchEnd` field — earlier substring-based redaction
  silently let through long jailbreak matches and synthetic unicode
  findings like `"U+202E"`. (#447)
- **`on_guardrail_triggered` hook dispatch** via context-attached
  `GuardListener` callback. Lookout stays zero-dep on the hooks
  package; the pipeline runner bridges them. Listener receives the
  full findings slice. (#447)

### Added — Tooling

- **`crewship apply` / `crewship export`** for declarative workspace
  manifests with sidecar service declarations (Redis, Postgres, MySQL,
  MongoDB). Migration v95 adds `crews.services_json`. (#448)
- **Playwright E2E specs:** `e2e/feedback.spec.ts` (8 contract tests)
  and `e2e/feedback-ui.spec.ts` (browser-side fetch via real NextAuth
  cookie + CSRF defense pin via spoofed Origin → 403). (#450)

### Added — Installation

- **Auto-generate secrets on first run.** `crewship start` writes
  NEXTAUTH_SECRET + ENCRYPTION_KEY to
  `~/.local/share/crewship/secrets.env` when missing. End users no
  longer touch env files for the happy path. (#446)

### Fixed

- **Online sampler was dead code in PR #447** — `NewOnlineSampler`
  had test coverage but no production call site. Wired into bootstrap. (#449)
- **Sampler SQL queried non-existent `completed_at` column.** Real
  column is `ended_at`. The test fixture matched the bug so unit
  tests passed; real schema check on dev-VM smoke caught it. (#449)
- **Code-scanning alerts.** All open CodeQL + Grype findings closed. (#445)
- **Privacy leak in `GET /api/v1/feedback`** — earlier draft scoped
  only by workspace membership; now scoped by `user_id` AND workspace. (#447)
- **Sanitize bypass via mixed zero-width characters.** ScanInput
  emitted a Finding only for the FIRST zero-width rune; subsequent
  ZWNJ/ZWJ/BOM in the same payload survived sanitize. Now emits one
  Finding per occurrence. (#447)
- **OnlineSampler data race** on watermark cursor between concurrent
  Start callers — `go test -race` reproduced. Added `sync.Mutex`;
  `Start` now wrapped in `sync.Once`. (#447)
- **Sampler panic-naked.** A panic in `runOnce` would kill the
  daemon. Added deferred `recover()` in `tickWithBackoff` that logs
  + lets the loop continue. (#447)

## [0.1.0-beta.2] — 2026-05-18

**First public beta release.** APIs and data models may break across
minor bumps until v1.0. See `RELEASING.md` for upgrade and rollback
guidance.

> v0.1.0-beta.1 was burned by a series of release-pipeline iterations
> (cosign version pin, pnpm toolchain mismatch in the Dockerfile,
> Windows cross-compile, missing direct deps for Turbopack,
> port_exposures test flake). The "release immutability" toggle was
> enabled mid-iteration and permanently reserved that tag name even
> after deletion. The first public tag is therefore v0.1.0-beta.2.

### TL;DR for beta testers

- Install: `brew install crewship-ai/tap/crewship` (macOS) or
  `docker pull ghcr.io/crewship-ai/crewship:v0.1.0-beta.2` (Linux/Docker).
- One adapter is production-ready in beta: **Claude Code (Anthropic)**.
  Codex / Gemini / OpenCode / Cursor / Factory Droid have scaffolds
  but lack parity testing — see README "Beta status & limitations".
- Telemetry (Sentry crash reporting) is **enabled by default** during
  v0.1 beta to give the solo maintainer signal from real installs.
  Disable any time with `crewship telemetry off`. Reverts to opt-in
  for v1.0 GA. See `RELEASING.md` Telemetry section.
- Storage is SQLite-only in v0.1; PostgreSQL is on the v0.2 roadmap.

### Added — Release infrastructure

- **Auto-snapshot before migrations.** `database.SnapshotBeforeMigrate`
  takes a `VACUUM INTO` copy as `<db>.pre-migrate-vN-to-vM-<UTC>.bak`
  whenever a migration is pending. Keeps 10 newest snapshots; opt out
  with `CREWSHIP_SKIP_MIGRATION_BACKUP=1`.
- **Migration lint in CI.** `.github/workflows/migration-lint.yml` +
  `scripts/lint-migrations` enforce append-only ordering — versions
  strictly increase, no rename of a version already shipped to `main`.
  In-tree Go tests guard monotonicity and uniqueness on every PR.
- **GHCR multi-arch Docker images.** linux/amd64 + linux/arm64,
  cosign keyless signed via GitHub OIDC. Tags published per release:
  `:vX.Y.Z`, `:vX.Y`, and `:latest` (last one ONLY on clean semver tags
  — pre-release tags never bump `:latest`).
- **Nightly channel.** `.github/workflows/nightly.yml` rebuilds on every
  push to `main`: `:nightly` and `:main-<sha>` Docker tags, plus a
  rolling `nightly` GitHub pre-release with prebuilt binaries.
- **One-line installer.** `scripts/install.sh` detects OS+arch, verifies
  sha256 + cosign signatures, installs to `~/.local/bin` (no sudo) or
  `/usr/local/bin`. Until the project website is live, fetch direct from
  the repo: `curl -fsSL https://raw.githubusercontent.com/crewship-ai/crewship/main/scripts/install.sh | bash`.
  The short `crewship.ai/install` redirect will land alongside the
  website launch.
- **Update notification.** `internal/update` queries GitHub Releases API
  daily (cached in `~/.crewship/cache`). CLI prints upgrade banner at
  startup; web UI surfaces a dismissable banner via
  `GET /api/v1/system/version`. Optional `GITHUB_TOKEN` to lift the
  60/h unauthenticated rate limit to 5000/h.
- **Sentry crash reporting (opt-out by default in beta).** New
  `internal/crashreport` package wraps `getsentry/sentry-go` behind a
  consent gate stored in `app_settings`. DSN injected at link time via
  ldflag from `SENTRY_DSN` GitHub Actions secret. Strict client-side
  scrubbing of headers, query strings, request bodies, User field, and
  device/runtime/culture contexts; server-side regex rules in Sentry UI
  cover email/Bearer/`sk-*`/`ghp_`/`xox*-` patterns in error messages.
  `CREWSHIP_SENTRY_DSN` env var redirects to a self-hosted/own Sentry.
- **`crewship telemetry on/off/status`** sub-commands manage consent at
  runtime; `status` shows the resolved endpoint host plus DSN source
  (vendor default vs env override). First-run prompt removed — beta
  default is enabled.
- **Sentry alert-rule provisioner** (`scripts/sentry-setup-alerts.sh`):
  idempotent bash script that calls the Sentry REST API to create the
  "New issue (beta)" and "Spike — 50+ events/hour" alert rules.
- **PR + repo hygiene.** Stale-bot workflow (issues 90d, PRs 44d, generous
  opt-out labels), PR template Migration Safety checklist,
  `scripts/setup-branch-protection.sh` one-shot for required checks +
  linear history. Hotfix runbook in `RELEASING.md`.
- **CODE_OF_CONDUCT.md** (Contributor Covenant 2.1 by reference) +
  `ee/README.md` scaffold for future dual-licensed enterprise add-ons.

### Added — Connectors (catalog → install → MCP)

- **`ConnectorCatalog`** tile-grid UI for browsing the bundled manifest
  catalog under `manifests/` (`feat/connector-catalog-impl`).
- **`SchemaForm`** five-field-type renderer (text/secret/select/toggle/
  number) with per-field validation and defaults.
- **`ConnectorConnectSheet`** wires SchemaForm into the install flow —
  validates inputs, persists credentials via the sidecar, hands off
  OAuth where applicable.
- **Backend connector handlers** — `ParseManifest`, `Validate`,
  `Resolve`, `MaterializeMCP`, `LoadAll`; HTTP routes for List / Get /
  Verify / Install (incl. credential persistence + OAuth handoff).

### Added — Auth + onboarding overhaul (PR #314)

Pre-beta sweep: account recovery, device pairing, split-screen
onboarding wizard, session-rotation + lockout primitives.

### Added — CLI: AI-first 2026 (15 new commands and flags)

Major CLI surface expansion aligning Crewship with the 2026 agent-CLI playbook (long-running workflows, plan/act separation, headless scripting, real-time dashboards, model-tiering control). All additions live in `cmd/crewship` and `internal/cli`; one server endpoint added (`GET /api/v1/runs/{id}`).

**New top-level commands:**

- **`crewship -p "..."`** — headless one-shot prompt to the default agent. Sets quiet by default, exits non-zero on agent error. Pipe-friendly: `cat issue.md | crewship -p "summarise"`.
- **`crewship plan <prompt>`** + **`--plan`** flag on `run`/`ask` — plan/act separation. Read-only architect mode that outputs a step-by-step plan + files-to-touch + risks without executing tools. Prompt-engineered (no backend mode), so it composes with every adapter.
- **`crewship resume [chat-id|run-id|pr-url]`** — pick up the last session, an explicit one, or the session that produced a GitHub/GitLab/Bitbucket PR. No-arg form opens a `huh`-styled picker over the 10 most recent CLI sessions.
- **`crewship wait <run-id>`** — block until a run reaches a terminal status. Status-aware exit codes (0 done, 1 failed, 2 cancelled, 3 timeout). Use in scripts: `crewship wait $(crewship ask --no-stream -q "..." | jq -r .id) && echo done`.
- **`crewship tui`** — real-time Bubble Tea dashboard. Three panels: running runs, pending approvals, live journal stream (SSE-pumped). Keys: `q` quit, `r` refresh, `Tab` focus.
- **`crewship recap <chat-id>`** — LLM-generated summary of a chat session via the default agent. Output is a 4-section markdown brief (outcome / decisions / open threads / next prompt). Tunable bullet count via `--bullets`.
- **`crewship shell`** — interactive REPL. Slash commands: `/help`, `/agent <slug>`, `/workspace <slug>`, `/cd`, `/plan` (toggle), `/effort <level>`, `/think` (toggle), `/clear`, `/history`, `/quit`. `@file` fuzzy expansion inlines file content into prompts.
- **`crewship me`** — your missions + your pending approvals + your recent runs (3 parallel REST calls).
- **`crewship today`** — today's runs and spend.
- **`crewship now`** — live status: running runs, idle/busy agent counts, pending approvals.
- **`crewship cost forecast`** — projected cost before you spend tokens. Two modes: `--prompt @file` (token-count heuristic) or `--from-history <agent>` (average of last 20 runs). Renders rate table for Sonnet 4.6 / Opus 4.7 / Haiku 4.5 with output-ratio tuneable (`--output-ratio`, default 2.0×).
- **`crewship diff <run-a> <run-b>`** — side-by-side comparison of two existing runs (status, agent, output diff). Distinct from `eval compare` which re-runs an eval scenario.
- **`crewship notify`** — desktop notifications group. `enable` / `disable` / `status` / `test` / `send <title> <body>`. Auto-fires on long-running run completion (≥30 s) and pending approvals. Uses `osascript` on darwin, `notify-send` on linux, BurntToast on Windows (no-op when missing).
- **`crewship slash`** — manage user-defined slash commands. `slash list` enumerates loaded files; `slash init` scaffolds `~/.crewship/commands/review.md` as a starter.

**New flags on existing commands:**

- **`--format=ndjson`** (global) — line-delimited JSON output, pipe-friendly for `jq -c` / `fx` / stream-processing tools. Plumbed through `Auto` / `AutoDetail` so every list/detail command supports it uniformly.
- **`--plan`** on `run` / `ask` — plan-mode without a separate command.
- **`--effort=minimal|low|medium|high|xhigh`** on `run` / `ask` — reasoning effort passthrough, threaded into chat-creation metadata.
- **`--show-thinking`** on `run` / `ask` — surfaces full reasoning blocks on stdout (not the 100-char truncated stderr peek).

**User-defined slash commands** (`~/.crewship/commands/*.md`)

Markdown files with YAML frontmatter become first-class CLI subcommands at load time:

```markdown
---
name: review
description: Review a diff
agent: viktor
plan: true
vars:
  - target
---
Review this ${target} for $args.
```

`name`/`description`/`agent`/`effort`/`plan`/`vars` are honoured. `$VAR` / `${VAR}` substitution against positional args. Built-in commands always win on collision (the loader skips + warns).

**Server surface (one endpoint added):**

- **`GET /api/v1/runs/{id}`** — single-run lookup used by `wait`, `resume`, `diff`. Reuses the existing `journal.ListRuns` + enrichment path; 404 for unknown ids (cross-tenant masked).

**New internal helpers** (single-responsibility, all unit-tested):

- `internal/cli/runs.go` — `GetRun(ctx, id)`, `PollRun(ctx, id, interval, onTick)`, `ParsePRURL(s)`, `RunDetail`.
- `internal/cli/notify.go` — `OSNotify(title, body, level)`, `NotificationsEnabled(cfg)`, GOOS dispatch matrix.
- `internal/cli/slashcmd.go` — `LoadSlashCommands()`, `ParseSlashFile(path)`, `SlashCommand.Render(args)`, frontmatter loader.
- `internal/cli/repl.go` — `REPL` struct with slash-dispatch, `ExpandAtFiles(line)`, `ApplyPlanShellPrefix`.
- `internal/cli/tui/` (package) — Bubble Tea Model/Update/View, SSE journal pump with reconnect, lipgloss styling.
- Formatter: `NDJSON(v)`, `WriteNDJSONRow(v)`, `"ndjson"` routing in `Auto` / `AutoDetail`.

**Tests added (~30 new tests):**

- `runs_test.go` — `IsTerminal`, `ParsePRURL` (5 hosts), `GetRun` (200/404/empty-id), `PollRun` (3-poll convergence).
- `notify_test.go` — `NotificationsEnabled` (nil/false/true), `OSNotify` no-panic guard.
- `slashcmd_test.go` — frontmatter parse, no-frontmatter fallback, `$VAR` / `${VAR}` / `$args` substitution, name validation.
- `repl_test.go` — slash dispatch, unknown-slash warning, `@file` expansion (existing/missing/`@-`), plan shell prefix idempotency.
- `formatter_ndjson_test.go` — slice → multi-line, single object → one line, `WriteNDJSONRow`, `Auto` routing.
- `cmd_run_metadata_test.go` — `SetEffort` validation (5 levels + uppercase + whitespace + invalid), `ChatCreationBody` (default vs plan vs plan+effort), `ApplyPlanFlag` idempotency.

**Documentation:**

- README links to new commands inline (TODO: separate `docs/cli/` page in a follow-up).
- This CHANGELOG entry doubles as the design rationale for each addition.

### Added — Routines: Eval framework (PR follow-up to #281–#284)

Cross-tier consistency framework that makes routines a credible **agentic-program primitive**. Three new pieces and one resurrected runner:

- **13 eval scenarios** seeded under the `eval-` prefix (`cmd/crewship/seeddata/eval_scenarios.go`). Each is a normal routine with rigorous gates — no special test-mode code path. Categories covered: pure transformation × 2, classification, format compliance, reasoning chain, prompt-injection refusal, RAG faithfulness, cross-family LLM judge, cost guardrail, boundary handling, DAG trajectory, idempotency / concurrency, tier-escalation loop. Cross-family graders (Sonnet judges Haiku) mitigate self-preference bias on rubric-graded scenarios.
- **`crewship eval scenarios`** — batch runner: sweep eval-* routines × tier list × N runs, output pass-rate matrix in `table` / `json` / `yaml` / `markdown`. Use `--scenarios slug,slug` to scope, `--tiers fast,smart` to compare worker tiers, `--runs N` for variance, `--fail-fast` for early-exit on regression.
- **`crewship eval compare <slug>`** — head-to-head: run ONE scenario back-to-back on two tiers, report a verdict (`AGREE-PASS` / `AGREE-FAIL` / `DIVERGE-A-PASS` / `DIVERGE-B-PASS` / `AMBIGUOUS`) plus side-by-side outputs. Designed for *gate-pass agreement*, not text identity (two LLM runs are essentially never byte-identical).
- **`tier_override` field on `RunInput`** + JSON body `{"tier_override":"..."}` on the `/run` endpoint. Replaces every `agent_run` step's `complexity` for the duration of one run; step-level `model_override` still wins. Plumbed through CLI as `crewship routine run --tier-override fast|smart|...`.
- **JSON Schema gate enforcement** in `internal/pipeline/executor.go validateOutput`. Previously a no-op (`"documentation only"`); now uses `github.com/santhosh-tekuri/jsonschema/v5` (draft 2020-12). Distinct reason prefixes per failure class: `schema invalid:` (author bug), `output not valid JSON:` (worker didn't follow contract), `schema validation:` (output failed constraints).
- **LLMRunner restored** (`internal/pipeline/runner_llm.go`) as opt-in fallback. Removed in commit `8408f3e6` when OrchestratorRunner shipped; restored here so the eval suite is runnable on a workstation without a fully provisioned crew container stack. Selection at boot: `CREWSHIP_PIPELINE_RUNNER=llm_direct` (explicit override) → `--no-docker` (auto-fallback) → OrchestratorRunner (default; production unchanged).
- **`schemas/routine.v1.json`** picks up `outcomes`, `concurrency_key`, `max_concurrent` so IDE validation matches the server-accepted DSL surface.

Tests: 8 schema-gate cases, 9 tier-override sub-cases, 10 eval-CLI helper tests, 13 eval-scenario parse+validate tests — 40 new test cases total, all under `-race`.

### Added — Routines (PR #281 + #282)

Routines are AI-authored, workspace-scoped declarative workflow recipes — one declarative layer that any crew can invoke for what previously required a patchwork of infra-as-code scripts, scheduled jobs, cron entries, chat-bot triggers, and ad-hoc shell SOPs. Authored once (preferably by a smart model) and executed many times by the cheaper runtime tier.

User-facing label is **Routine**; backend identifiers (`pipelines` table, `internal/pipeline` package, `/api/v1/.../pipelines/...` HTTP routes) remain unchanged for backwards compat. Three-layer architecture: **Routine** (atomic) → **Recipe** (Marketplace template, future) → **Cyclic Issue** (recurring user issue, future).

#### Frontend

- **New `/routines` page** as a clone of `/orchestration`. 3-column layout: filter sidebar with saved-view facets (status / usage / authored-by / show ephemeral), 4 main tabs (Routines list / Graph / Timeline / Activity), right detail panel with 7 sub-tabs (Overview / Editor / Runs / Versions / Schedules / Webhooks / Waitpoints).
- **Sidebar entry** *Routines* under *Work* (icon `ScrollText`).
- **Orchestration tab** *Routines* — 5th tab in `/orchestration` for in-context discovery, reusing the existing detail sheet so users don't lose mission context.
- **DSL editor dialog** — paste/edit JSON with 3 starter templates. **Test & Save** runs `/test_run` first; on pass calls `/save`. Skip-test-gate checkbox surfaces only for OWNER/ADMIN roles.
- **Run / Test Run / Dry Run / Cancel** action toolbar.
- **Live waterfall** — Runs sub-tab subscribes to `pipeline.step.*` WebSocket events; auto-expands the most recent run on first visit.

#### Backend

Five database migrations: v78 (`pipelines` + `workspaces.execution_tiers_json`), v79 (`pipeline_versions` + `pipeline_waitpoints`), v80 (`pipeline_schedules`), v81 (`pipeline_run_idempotency`), v82 (`pipeline_webhooks`).

- **6 step types**: `agent_run`, `call_pipeline`, `http`, `code`, `wait`, `transform`.
- **DAG with `needs[]`** — independent steps execute in parallel; leaf-node final-output preference for multi-leaf graphs.
- **Conditional `if`** — any step can carry a template-rendered boolean; false → step skipped.
- **Two-tier execution** — workspace `execution_tiers_json` resolves `complexity` annotation to `(adapter, model)`; tier override flows through to the CLI adapter's `--model` flag.
- **Versioning + rollback** — every save creates a new immutable version; rollback creates a new HEAD pointing at the target's definition.
- **HITL waitpoints** — DB-backed approval primitive with timeout sweeper and boot-time recovery scan reporting stranded entries.
- **Cron schedules** + **HMAC-signed webhooks** + **idempotency keys** for safe redelivery.
- **Bundle export/import** for cross-workspace transfer.
- **Workspace-scoped `POST /api/v1/workspaces/{ws}/pipelines/save`** for UI authoring (MANAGER+ role); `skip_test_gate` flag honoured only for OWNER/ADMIN.
- **8 stability bug fixes** with regression tests under `-race`: DAG completion bookkeeping, multi-leaf output picker, waitpoint lost-wakeup, webhook rate-limiter race, idempotency stale-row leak, SSRF-via-redirect, cross-workspace agent execution, template validation breadth, exponential-backoff jitter.

#### CLI (17 routine subcommands)

| Group | Commands |
|-------|----------|
| Core | `list`, `get`, `save`, `run`, `dry-run`, `delete`, `runs` |
| Versions | `versions`, `rollback --to N` |
| Bundles | `export [--include-history]`, `import [file.json]` |
| Runs | `cancel`, `watch [--json] [--once]` |
| Authoring | `validate [file.json]` (offline DSL check, CI-friendly) |
| Schedules | `list`, `create`, `update`, `enable`, `disable`, `now`, `delete` |
| Webhooks | `list`, `create`, `url`, `delete` |
| Waitpoints | `list`, `show`, `approve`, `reject` |

The `pipeline` alias is preserved — every `crewship routine X` invocation also works as `crewship pipeline X`.

#### Documentation

- `docs/guides/routines.mdx` — user guide (concepts, three authoring paths, DSL anatomy, all step types, two-tier execution, triggers, HITL, validation gates, observability, RBAC, troubleshooting).
- `docs/cli/routine.mdx` — per-subcommand reference.

#### Seeded routines

`./dev.sh seed` now populates 5 starter routines on a fresh workspace: `summarize-text`, `fetch-and-summarize`, `pr-review-structured`, `daily-status-digest`, `incident-triage`. Each is independently runnable with default inputs.

### Added — Core platform

- Self-hosted runtime: single Go binary with embedded Next.js UI, embedded
  SQLite DB, and a sidecar proxy for credential injection.
- Crew Journal — append-only event stream as canonical source of truth
  for every observable action; FTS5 search; SSE streaming to the
  `/journal` UI.
- Paymaster — hierarchical LLM cost budgets (workspace → crew → mission →
  agent), per-call ledger written before the LLM request leaves the box.
- Lookout — guardrails: prompt-injection detection, JSON-schema tool-arg
  validation, output parsing, secrets redaction.
- Harbormaster — human-in-the-loop approval queue with sync and async
  modes, configurable timeouts, full decision history.
- Cartographer — checkpoint/fork/restore over journal cursor; non-
  destructive restore returns divergence warnings instead of mutating.
- Quartermaster — eval suite with trajectory replay, regression detection,
  and an LLM-as-judge that uses rubric-shuffle anti-bias.
- Hooks framework — 15 lifecycle event types with shell, HTTP, and
  subagent handlers; `allowedShell=true` required at register time.
- Backup — AGE-encrypted, portable `.tar.zst` bundles at workspace and
  crew scope; retention rotation; advisory locking; forward-compatible
  manifest.
- Keeper — credential gatekeeping with AES-256-GCM versioned encryption
  and an Ollama-backed LLM evaluating per-request access.
- Multi-runtime container support — auto-detection of Docker, Podman,
  Colima, OrbStack, Rancher, nerdctl. Apple Containers on macOS Tahoe+.
- CLI adapters — Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor
  CLI, Factory Droid, all wired into the orchestrator dispatch table.
- Crew templates — Engineering, Quality, DevOps, and Research crews seed
  ready out of the box; `crewship template apply <slug>` to deploy.
- Issue tracker — Linear-style with labels, projects, sub-issues, and
  bulk operations; `crewship issue …` CLI.
- Multi-workspace support; OWNER/ADMIN/MANAGER/MEMBER/VIEWER server-side
  RBAC enforcement (UI for tier assignment ships in v0.2).
- OpenTelemetry GenAI spans with W3C trace-context propagation; OTLP HTTP
  exporter; every journal entry carries `trace_id`/`span_id`.
- Devcontainer provisioning with mise-managed runtimes, shared cache
  images, and 24-hour registry-digest checks.
- Per-IP rate limiting (10 req/min on auth endpoints, 120 req/min on the
  general API), security headers, single-use OAuth state with 15 min
  expiry.
- Goreleaser pipeline: cross-compiled binaries (Mac amd64+arm64, Linux
  amd64+arm64, Windows amd64), keyless cosign signatures, SPDX +
  CycloneDX SBOMs, Homebrew tap auto-publish.

### Security — Pentest 2026-05-14 hardening pass

Internal pentest of `dev2` (`dev-server:8082`, build `a78e8ac`)
produced 11 findings across 7 surfaces. All fixes have PoCs that
confirm the bypass before and the block after (reports gitignored
under `.pentest-2026-05-14/`).

- **F-001 (HIGH):** SSRF in skills import via DNS-resolved hostname
  bypass — blocked.
- **F-002 (MEDIUM):** SSRF error messages leaked internal network
  state — generic error masking.
- **F-003 (MEDIUM):** `/metrics` exposed without auth — now gated.
- **F-004 (LOW):** Next.js SPA fallback masked 404 for sensitive paths.
- **F-005 (INFO):** Inconsistent path-traversal validation — unified.
- **F-006 (MEDIUM):** No backend Origin check on state-changing routes.
- **F-007 (HIGH):** Rate limiter bypassable via X-Forwarded-For
  rotation — IP resolution hardened.
- **F-009 (LOW):** Scrubber regex bypassable via zero-width characters.
- **F-011 (HIGH conditional):** Devcontainer features could request
  `Privileged` / dangerous `CapAdd` — denylist applied.
- **F-012 (MEDIUM):** `CREWSHIP_DISABLE_RATELIMIT=true` shipped in dev
  `.env.local`.
- **F-A1/A3/A4 (HIGH):** Workspace-IDOR on relations + parent_issue_id
  — workspace-scope enforcement.
- **F-B4 (LOW):** Capability-URL proxy leaked `Referer` to upstream.
- **G-002:** Memory injection guard hardening.

### Security — Pass-2 quickfixes

Four backlog items bundled (each <70 LOC, independently revertible):

- Sidecar credential reads now emit audit events.
- Emoji reactions XSS — payload validation tightened
  (`emoji_reaction_test.go` covers 24 cases including real XSS strings).
- `/admin/backups/metrics` redacted to drop cross-owner workspace IDs.
- WebSocket frames capped at 1 MiB; fan-out N-amplifier closed.

### Security — Supply chain

- All release artifacts signed with cosign keyless via GitHub Actions
  OIDC (SLSA-3-ish provenance chain). Verify with
  `cosign verify-blob --certificate-identity-regexp ...`.
- SBOMs in SPDX and CycloneDX shipped with every release.
- `migration-lint` CI gate prevents the rebase-collision class of
  schema-divergence bug that bricks customer DB on upgrade.
- Goreleaser builds are reproducible (`-trimpath`, fixed `GOFLAGS`).
- `gitleaks` + `govulncheck` + `grype` run on every PR via
  `.github/workflows/security.yml`.

### Changed

- **README** rewritten for honest beta status — every feature labeled
  ✅ stable / 🟡 early / 🚧 WIP. Adapter scaffolds for non-Anthropic
  CLIs explicitly marked WIP rather than equal-billing alongside the
  production-tested Claude Code adapter.
- **Distribution channels** documented in `RELEASING.md` — stable /
  beta / nightly with their respective Docker tag policies. `:latest`
  Docker tag only moves on clean semver tags; pre-releases NEVER
  overwrite `:latest`.
- **Hotfix workflow** documented in `RELEASING.md`: cherry-pick onto
  release branch, fix-forward (never untag), forward-port to `main`.

### Removed — Repo hygiene (PR #344, #348)

- `.claude/context/prd/*` and `.claude/context/wireframes/*` —
  ~52 000 lines of pre-implementation design docs untracked.
  Mintlify (`docs/`) is now the canonical user-facing docs source.
- `internal-docs/audit-archive/*`, `internal-docs/wireframes/*` —
  archived audit reports and HTML wireframes.
- `mockups/activity-rail-v{2,3}.html` — wireframes for the
  activity-rail feature shipped in #287.

### v0.2 roadmap

The following ship as packages but are not yet auto-wired into the
runtime in v0.1; they activate via manual API calls today and become
default behaviour in v0.2:

- Episodic memory — vector recall over the journal (selective embedding,
  SQLite BLOB cosine).
- Consolidate — daily Consolidator that extracts learned rules into
  crew memory + Compactor that rolls up low-signal old entries.

The following are planned for v0.2 but not in v0.1 at all:

- PostgreSQL primary database (SQLite is the only supported backend in
  v0.1).
- Kubernetes container provider.
- Skills marketplace (local skill imports work today).
- Workspace-scope memory tier (3-tier today: agent, crew, session).
- Stripe-backed billing tiers / edition gating (v0.1 ships fully
  Apache-2.0 with no edition gating).
- UI for assigning ADMIN/MANAGER/VIEWER workspace roles (server-side
  enforcement is already wired).
- Crew-to-crew handoff with critique exchange.

### Notes

- This is the first tagged release. Public APIs and data models may
  still change in `0.x` minor versions before `1.0`. Pin a commit SHA or
  a specific `v0.x.y` tag if you ship to production.
- The `release` branch tracks deployable state (a 5-minute systemd timer
  on the dogfood prod VM polls it). Push `main:release` to deploy.

[Unreleased]: https://github.com/crewship-ai/crewship/compare/v0.1.0-beta.2...HEAD
[0.1.0-beta.2]: https://github.com/crewship-ai/crewship/releases/tag/v0.1.0-beta.2
