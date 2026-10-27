# Native Craft human sessions and edit leases

The separate whole-native WordCraft/GridCraft profile is experimental and off by
default. `craft_native.enabled: true` or `CREWSHIP_CRAFT_NATIVE_ENABLED=true`
enables its human session and EditLease HTTP API. It requires a Linux boot-aware
clock. This does not activate New Doc, convert existing documents, enable agent
proposals, or qualify the complete editor acceptance.

Availability requires a readable Page with an explicit `craft_native_documents`
profile. Other profiles, inaccessible Pages and absent Pages report disabled.
Ordinary authentication and workspace membership remain required.

## Routes

All paths below start with `/api/v1/pages/{slug}/craft`. Supply the workspace
through `?workspace_id=...`; `@<page-id>` is accepted in place of a slug.

| Method | Suffix | Operation |
|---|---|---|
| GET | `/availability` | `{enabled: boolean}`; no Craft admission required. |
| POST | `/sessions` | Body `{}`; issue a new mounted human admission, status 201. |
| POST | `/sessions/reauthorize` | Use independently refreshed authentication for the same live admission. |
| GET | `/lease` | Current holder, generation and handoff requests. |
| POST | `/lease` | Acquire when free. |
| POST | `/lease/acquire` | Explicit alias for acquisition. |
| POST | `/lease/renew` | Renew this admission's current generation. |
| POST | `/lease/release` | Release this admission's current generation. |
| POST | `/lease/request-handoff` | Request custody from the current holder, status 201. |
| POST | `/lease/handoff` | Transfer to an existing public request ID. |
| GET | `/events` | Authenticated SSE metadata and live-holder heartbeat. |

Except availability and initial session creation, send `X-Craft-Session` as well
as ordinary authentication. Session creation returns `session_token`, the distinct
public `session_id`, `application_id`, and `page_id`. Keep the token only in the
mounted host closure, never in editor messages, local drafts, URLs or public DTOs.
The editor host and its lease controls share one admission; separate admissions
represent separate connections even for the same human.

Renew/release require `{generation: "1"}`. Handoff requires
`{generation: "1", request_id: "public-request-id"}`. Other lease mutations use
`{}`. Generations are canonical positive decimal **strings**, never JavaScript
numbers. Before the first acquisition, the status generation is `null`.

A lease status has `holder: {actor_id, display_name, kind} | null`, `mine`,
`generation`, `expires_at`, `can_write`, `acquired_at`,
`handoff_requested_kind`, and `handoff_requested_by`. Requests are an **array**
of `{request_id, actor_id, display_name, kind}`, visible to the holder. Current
admissions are human-only. `acquired_at` is presently `null`: the lease does not
persist acquisition wall time, and renewal time must not impersonate it.

Competing acquisition and stale generations return 409. A handoff advances the
server generation; a late renew or write from the old connection is rejected.
Neither a display name nor a public request ID establishes write authority.

## Authorization and lifetime

Every operation revalidates the original current human credential, workspace,
Page rights and captured authority epochs. Lease mutations also recheck current
authority after SQL publication. Runtime tokens are hashed in memory; a process
restart invalidates them. Durable lease generations and restore fences prevent
old custody from being revived by backup replacement.

The SSE connection renews only its own authorized current holder every five
seconds, with a fifteen-second lease. Browser focus does not control custody.
No document bytes travel through this stream. `lease` events contain advisory
status; `session_retired` stops delivery and explicitly says `draft: "retain"`
or `draft: "purge"`. Generic 401/403 responses alone do not prove read loss and
must not trigger local erasure. Unclassified authority changes conservatively
retire the old admission; a fresh read admission is separate from reacquisition.

Original expired or revoked credentials never regain an old admission. Explicit
reauthorization accepts only a separately authenticated newer expiry for the same
still-live identity and epoch. It never extends an already exhausted boot-time
cap or adopts another source identity.

## Current implementation boundary

The backend has direct-handler and real-Router two-human regression coverage.
Native whole-document storage, pinned engine validation and receipts are a
separate disabled boundary. Full editor mounting, proposals and agent execution,
one-hour browser acceptance and 2,000 changing saves remain separate work.
