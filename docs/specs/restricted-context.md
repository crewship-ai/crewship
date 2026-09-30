# Restricted conversation and run context

Dedicated restricted routes derive the actor from the authenticated session and
workspace from the selected workspace. They never accept a policy, principal,
provider key, model, runtime handle or source provenance from request metadata.
Unclassified crew filesystem, journal, logs and shared memory remain unavailable
in restricted mode. A model profile is operator configured; a grant does not
install or activate a runtime.

Context is rechecked against live membership, grant revisions and source
provenance. Private conversations belong to their creator. Group text context
also captures current participants and a causal generation; native execution
requires a private conversation. Notes and derived outputs cannot outlive their
source authority. These APIs return bounded current projections, not a full
historical export or an automatic summarization service.

## Wire objects

A context entry contains `id`, `created_at`, `kind`, `role`, `content`, and
optional `created_by` and `sources`. A file contains `id`, `name`, `size_bytes`,
`sha256`, and `created_at`. An attempt outcome contains only `state`,
`created_at`, and `recorded_at`; no prompt, result, error, provider or runtime
capability is published. Error responses use JSON `{"error": "…"}`.

## GET

```http
GET /api/v1/agents/{agentId}/restricted-execution
```

Authentication: Agent edit permission in the selected workspace.

Request: No request body.

Response: 200 JSON `{"profile": "disabled|responses_text|native_api_key"}`.

Statuses: 401 unauthenticated, 403 edit denied, 404 absent target, 503 unavailable storage.

## PUT

```http
PUT /api/v1/agents/{agentId}/restricted-execution
```

Authentication: Agent edit permission in the selected workspace.

Request: JSON `profile` equal to disabled, responses_text or native_api_key.

Response: 200 JSON `{"profile": "…"}`. Profile changes revoke currently bound attempts; credentials and model authority remain separately checked.

Statuses: 400 invalid profile, 401 unauthenticated, 403 edit denied, 404 absent target, 503 storage unavailable.

## GET

```http
GET /api/v1/agents/{agentId}/run-profile
```

Authentication: Authenticated membership and exact agent.run in restricted mode.

Request: No request body.

Response: 200 JSON `{"mode": "trusted|restricted"}`. This does not prove an installed runner.

Statuses: 401 unauthenticated, 404 unavailable target or authority.

## POST

```http
POST /api/v1/agents/{agentId}/restricted-cli-chats
```

Authentication: Authenticated restricted membership and exact agent.run.

Request: A required empty JSON object `{}`; at most 256 request bytes.

Response: 201 JSON `{"id": "new-chat-id"}` for a fresh private CLI origin. It requires run authority independently from chat authority.

Statuses: 400 nonempty or malformed body, 401 unauthenticated, 404 target unavailable, 503 storage unavailable.

## GET

```http
GET /api/v1/chats/{chatId}/execution-profile
```

Authentication: Current chat audience; an owning private CLI caller may use its exact agent.run grant.

Request: No request body.

Response: 200 JSON `{"mode": "trusted|restricted", "audience": "private|group|workspace"}`.

Statuses: 401 unauthenticated, 403 membership unavailable, 404 chat unavailable.

## POST

```http
POST /api/v1/chats/{chatId}/restricted-run
```

Authentication: Current chat audience and exact agent.chat; host-owned configured restricted profile.

Request: JSON `content` (nonempty, maximum 32768 UTF-8 bytes), optional `project_file_versions` (up to 16 unique immutable IDs, each at most 96 bytes). Project selection requires the native profile and current project.read for every source. Unknown JSON fields are rejected.

Response: 200 text/event-stream with `data:` JSON `{"type":"text","text":"…"}` frames followed by `{"type":"done","text":""}`. The stream closes on later failure; missing done must be treated as incomplete.

Statuses: 400 invalid input, 401 unauthenticated, 403 denied before delivery, 503 missing runtime. Each delivered frame rechecks live authority.

## POST

```http
POST /api/v1/chats/{chatId}/restricted-cli-run
```

Authentication: The private context owner and exact agent.run; a chat grant alone does not authorize this operation.

Request: Same bounded JSON body as restricted-run, including explicitly selected native project versions.

Response: 200 text/event-stream with text and done frames; later errors close the stream. No silent fallback to a shared crew executor.

Statuses: 400 invalid input, 401 unauthenticated, 403 denied before delivery, 503 missing runtime.

## GET

```http
GET /api/v1/chats/{chatId}/restricted-context
```

Authentication: Current scoped conversation audience and exact agent.chat.

Request: Optional query `kind` (history, memory or summary) and `search` (maximum 256 bytes).

Response: 200 JSON `{"entries": [...], "limit": 256}` from the latest 256 authorized entries. Filters apply to that bounded current projection.

Statuses: 400 invalid query, 401 unauthenticated, 404 context unavailable.

## POST

```http
POST /api/v1/chats/{chatId}/restricted-memory
```

Authentication: Current scoped conversation audience and exact agent.chat.

Request: JSON `content`, nonblank and at most 8192 UTF-8 bytes; unknown JSON fields rejected.

Response: 201 JSON context entry with server-derived creator, time and scope. Notes are recalled only within their captured conversation authority.

Statuses: 400 invalid content, 401 unauthenticated, 404 context unavailable.

## DELETE

```http
DELETE /api/v1/chats/{chatId}/restricted-memory/{entryId}
```

Authentication: Current scoped conversation audience; the selected note must belong to the caller.

Request: No request body; exact immutable entry ID in path.

Response: 204 with no response body. Deletion revokes descendants that consumed the note.

Statuses: 401 unauthenticated, 404 note or authority unavailable.

## GET

```http
GET /api/v1/chats/{chatId}/restricted-files
```

Authentication: Current scoped conversation audience and exact agent.chat.

Request: No request body.

Response: 200 JSON `{"files": [...]}` from at most 256 candidate entries, filtered by current exact source provenance. Metadata is rechecked before delivery.

Statuses: 401 unauthenticated, 404 files unavailable.

## GET

```http
GET /api/v1/chats/{chatId}/restricted-files/{fileId}/download
```

Authentication: Current scoped conversation audience and current exact file/source authority.

Request: No request body; immutable file ID in path.

Response: 200 application/octet-stream, attachment filename and X-Content-Type-Options nosniff. Current authority is rechecked between bounded 16KiB chunks.

Statuses: 401 unauthenticated, 404 file unavailable. A later revocation terminates delivery.

## GET

```http
GET /api/v1/chats/{chatId}/restricted-attempts
```

Authentication: Current private actor, scoped chat generation and exact agent.chat.

Request: No request body.

Response: 200 JSON array of at most 100 content-free outcomes for this actor and current conversation generation. States are completed, failed, canceled or denied.

Statuses: 401 unauthenticated, 404 chat unavailable.

