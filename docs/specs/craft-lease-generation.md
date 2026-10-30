# Craft lease generations, handoff and agent custody

This spec describes the single-editor lease that guards native Craft Word and
Sheet artifacts: how a lease generation is issued, renewed, released and handed
off, how an agent task takes the same lease, and which status codes a client
sees when custody changes underneath it. The complete route list, session
admission, whole-document save contract and agent task lifecycle live in
[Native Craft sessions, leases, agent tasks and whole-document API](craft-native.md);
this page narrows in on custody and does not repeat them.

## Availability and switches

Everything here is off by default.

| Switch | Kind | Default | Effect |
|---|---|---|---|
| `craft_native.enabled` / `CREWSHIP_CRAFT_NATIVE_ENABLED=true` | Instance config, read at startup | off | Serves the human session and lease API. When off, lease mutations return 503 and lease reads return 403. Requires a Linux boot-aware clock and at least one qualified Word or Sheet profile. |
| `craft_native.agents_enabled` / `CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED=true` | Instance config, read at startup | off | Lets an explicitly consented agent task take the lease. Enabling the human editor alone grants agents nothing. |
| `NEXT_PUBLIC_CHAT_CRAFT_LEASE=true` | Frontend build variable | `false` in the production build | Shows the lease bar (who is editing, request handoff) for a Craft artifact opened from chat. |

There is no workspace feature flag for native Craft. Restarting the server with
a changed instance configuration is the only way to switch it.

The API still addresses a Craft document through its Page slug
(`/api/v1/pages/{slug}/craft/...`). The slug also accepts `@<page-id>`. This is
a routing detail; in the product, Word and Sheet documents are artifacts.

## The lease model

A lease is one row per document. It records the holder, the generation and an
expiry measured on the server's boot clock. At most one holder can write at a
time; everyone else with access reads.

- **Holder.** A holder is an actor plus a session. A human holder is
  `user:<user-id>` bound to one Craft admission (one `X-Craft-Session` token).
  Two browser tabs of the same person are two different sessions, so the second
  tab cannot write while the first holds the lease. An agent holder is
  `agent:<agent-id>` with the session `native-agent:<run-id>`.
- **Generation.** A positive 64-bit integer, sent and returned as a canonical
  decimal **string** (`"1"`, never `1`, `"01"` or `"+1"`). It is the fencing
  token for every write.
- **Time to live.** Fifteen seconds. The editor's event stream renews its own
  lease every five seconds while the connection is authorised. Browser focus
  and typing do not affect custody.
- **Restart.** A lease is live only on the server boot that issued it. After a
  restart every previous lease reads as free, and every Craft admission must be
  opened again.

### How the generation moves

| Event | Generation | Holder after |
|---|---|---|
| Acquire when the lease is free or expired | previous + 1 (first acquisition: 1) | caller |
| Acquire again by the current holder session | unchanged | caller |
| Renew | unchanged | caller; expiry moves forward |
| Release | unchanged | none |
| Handoff to a requester | previous + 1 | requester |
| Revoke (agent task stopped or retired) | unchanged | none |
| Server restart | unchanged in storage | none (reads as free) |

Generations never go backwards and are never reused for a document. Because a
release keeps the stored number and the next acquisition adds one, a client
that kept an old generation cannot renew, release, save or hand off with it
after anyone, including itself, has acquired again. A lease status read
before the first acquisition reports `generation: null`. After a release it
still reports the last generation, with `holder: null` and `mine: false`.

Losing Page edit rights does not revoke a human's lease. The holder's writes
fail the authority check, and the lease expires on its own.

### Writes are fenced by generation and version

A save, import, restore, share or agent apply carries both the lease
`generation` and the `baseVersion` it was prepared on. Inside one database
transaction the server:

1. rechecks the original human or agent authority and the Page rights,
2. checks that the stored lease has the same holder, the same generation and
   has not expired on the current boot,
3. compares `baseVersion` with the latest stored version (compare-and-set), and
4. for a save, import, restore or agent apply, publishes exactly one new
   version, then rechecks authority and the lease again before commit.

Share publishes nothing. It selects an existing version as the shared version
and returns that version. A mismatch in step 2 or 3 returns **409** and
changes nothing. Saved versions are never overwritten; a restore publishes a
new version.

## Human lease operations

All lease routes need ordinary authentication, `workspace_id`, and the
`X-Craft-Session` admission header. Reading the lease needs Page read access.
Acquire, renew, release, request-handoff and handoff need current Page edit
rights; with a CLI token that means the `pages:write` scope. Request bodies are
strict JSON objects of at most 2 KiB; unknown fields return 400.

| Operation | Route | Body | Success |
|---|---|---|---|
| Read | `GET /api/v1/pages/{slug}/craft/lease` | none | 200, lease status |
| Acquire | `POST /api/v1/pages/{slug}/craft/lease` or `POST /api/v1/pages/{slug}/craft/lease/acquire` | `{}` | 200, lease status |
| Renew | `POST /api/v1/pages/{slug}/craft/lease/renew` | `{"generation":"7"}` | 200, lease status |
| Release | `POST /api/v1/pages/{slug}/craft/lease/release` | `{"generation":"7"}` | 200, lease status (free) |
| Request handoff | `POST /api/v1/pages/{slug}/craft/lease/request-handoff` | `{}` | 201, `{"request_id":"…","generation":"7"}` |
| Hand off | `POST /api/v1/pages/{slug}/craft/lease/handoff` | `{"generation":"7","request_id":"…"}` | 200, lease status of the new holder |

An empty body is malformed: acquire and request-handoff need the literal `{}`.

The lease status fields (`holder`, `mine`, `generation`, `expires_at`,
`can_edit`, `can_write`, `acquired_at`, `handoff_requested_kind`,
`handoff_requested_by`, `can_stop_agent`) are defined in
[craft-native.md](craft-native.md#routes). Two points matter for custody:
`can_write` is true only for the session that holds the lease and still has
edit rights, and `expires_at` is advisory wall-clock text. The server decides
expiry with its boot clock, not with the value a client sends or displays.

### Handoff

Handoff is cooperative. Nobody can take a live lease away from another human.

1. A session that does not hold the lease, but has edit rights, calls
   request-handoff. The request is recorded against the **current**
   generation and returns a `request_id`.
2. The holder sees the request in `handoff_requested_by`. Only the holder sees
   it, and only while it still matches the current generation and the
   requester's admission is alive.
3. The holder calls handoff with its own current generation and that
   `request_id`. The lease moves to the requesting session with generation + 1,
   and all other pending requests for the document are dropped.

A request is tied to one generation. Once the generation moves on (the holder
releases and someone acquires again, the lease expires and is re-acquired, or
another handoff happens), the old request no longer applies and the requester
must ask again. A requester that already holds the
lease, or asks while the lease is free or expired, gets 409: acquire a free
lease instead. Requests are kept in the server process as well as in storage;
a restart clears them together with the leases.

Before handing off, the holder's editor should settle any save whose outcome
it has not confirmed and keep its local draft. Handoff only moves custody. It
never saves or discards content.

### Status codes

| Status | Message | When |
|---|---|---|
| 400 | `invalid native lease request` | Body missing, not a JSON object, over 2 KiB, or with unknown fields. |
| 400 | `invalid generation` | Renew, release or handoff with a generation that is not a canonical positive decimal string. |
| 403 | `native Craft access denied` | No valid admission, the admission was retired, rights were lost, handoff with an unknown `request_id` or a `request_id` registered for another generation. Also returned for unexpected server-side failures, so a 403 alone does not prove that read access is gone. |
| 409 | `native editor lease changed` | Someone else holds a live lease (acquire), or the generation is no longer the holder's live generation (renew, release, handoff), or request-handoff is not applicable. |
| 503 | `native Craft unavailable` | Lease mutation while native Craft is switched off. |

Document routes that publish versions use their own message for the same
conflict: **409** `native document or lease changed; keep the local draft`, and
**413** `native history quota reached; existing versions retained` when the
history byte budget is full.

## Agent custody

An agent edits a Craft document only inside a native agent task that a human
started with explicit whole-document consent (see
[Native agent tasks](craft-native.md#native-agent-tasks)). The agent uses the
`craft_read`, `craft_acquire`, `craft_renew`, `craft_apply` and
`craft_release` MCP tools. The host binds every call to that one task; the agent
cannot choose the document, actor, run or profile.

- **Same lease.** `craft_acquire` takes the same lease as the human editor,
  under the agent's own identity. It never borrows a human session.
- **A human holder wins.** If any live holder exists, acquisition returns 409
  with `{"error":"document already has an editor","holder_kind":"human"}` (or
  `"agent"`). Nothing is applied and no version is published. While an agent
  holds the lease, a human editor that tries to acquire gets 409
  `native editor lease changed`.
- **Generation per task.** The host remembers the lease the task acquired.
  Renew, release and apply must send exactly that generation. Any other value
  returns 409 `native document or lease changed; keep the local draft`.
- **Keepalive.** Between calls the agent must renew within fifteen seconds.
  While one apply is running, the host renews that exact lease every five
  seconds for at most 65 seconds.
- **Release.** `craft_release` returns `{"released":true}`. Acquire and renew
  return `{generation, expires_at, holder_kind:"agent"}`.
- **Stop.** The human who started the task can stop it. Stopping revokes the
  agent's lease (the generation stays the same, the holder becomes empty) and
  closes the task's grant before the response. A stop never forces a human
  holder.

### Apply outcomes

`craft_apply` carries `generation`, `base_version`, a new `request_id`
(1–128 characters from `A–Z a–z 0–9 _ . : -`) and 1–64 commands.

The agent's sidecar checks every MCP call before it reaches the host:

- A JSON-RPC body over 128 KiB, or one that is not a valid JSON-RPC 2.0
  request, gets a JSON-RPC error `invalid bounded request` with HTTP 400.
- Tool arguments over 96 KiB, an unknown field, a generation or
  `base_version` that is not a canonical positive decimal string, a bad
  `request_id`, or a `commands` list that is empty, longer than 64 entries or
  over 64 KiB get a tool result 400
  `explicit bounded native operation required`.
- A call without a live task token gets 403 `live native Craft task required`.

Calls that pass these checks are forwarded to the host. The table lists what
the agent then receives as the tool result.

| Status | Meaning | What the agent does |
|---|---|---|
| 200 | Committed receipt with the new version. Only this proves Saved. | Continue from the new version. |
| 400 | Rejected by the sidecar checks above, or by the host as `invalid native Craft operation`. | Fix the request. |
| 403 | `confirmed native Craft task required`: the call is not bound to a live, confirmed task. | Stop. |
| 409 | Lease generation changed, `base_version` is not the latest version, or the same `request_id` was reused with different content. | Read again; do not reuse the `request_id` for new content. |
| 413 | History byte budget full, or the task already used its 64 apply IDs. | Stop and report. |
| 422 | The original engine refused the commands or produced an invalid document. | Change the commands. |
| 503 | `original native save outcome unconfirmed; reconcile the exact original request`. The task is blocked for new edits until the uncertain attempt is reconciled. | Resend the exact original request (same `request_id` and content). That only reconciles the first attempt; it never runs the engine again. |

When the sidecar cannot reach the host, it returns 503
`native task outcome uncertain; retain exact original request` with the
forwarded request echoed back. Treat it the same way: the result is unknown,
not failed.

## Quotas and retention

Native Craft history uses the artifact version store. Each version holds one
complete native document of at most 32 MiB (the JSON request body may be up to
45 MiB because the bytes are base64 encoded). The default history budget per
document is 16 GiB. Retention is `keep-all`: nothing is pruned, and when the
budget is full new saves return 413 while every existing version stays
readable. The read response reports the budget as `quota.usedBytes`,
`quota.limitBytes`, `quota.warning` (set from 80 % use) and `quota.retention`.

## Not covered: Yjs collaboration

Native Craft documents do not use Yjs or any other live merge. The lease
enforces one writer at a time, and the version store keeps a linear history.
Design artifacts store Yjs state in their versions; that model does not apply
here. The Craft authority check has no collaboration-document dependency.
