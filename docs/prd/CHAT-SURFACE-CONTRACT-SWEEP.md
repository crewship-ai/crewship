# Chat surface — contract sweep

**Branch:** `feat/chat-primary-surface`
**Date:** 2026-08-13
**Scope:** the three things the chat work has not had done to it — the
endpoint ↔ CLI ↔ doc triple for every route this branch added or changed, the
caps it applies without saying so, and a re-verification of the load-bearing
claims in `CODEX-WORK-ORDER-CHAT-1-0.md` §3 and `chat-as-a-primary-surface.md`
§4 and §6.

This document changes no code. Where a fix is needed it is written out
precisely enough to apply in one pass, because three other agents are editing
`internal/api/`, `internal/askforms/`, `cmd/crewship/` and both PRDs while this
was written and a survey that also edits is a survey that causes merge damage.

**Companion:** `CHAT-SURFACE-CODE-AUDIT-2026-08-13.md` (another agent, in
flight) covers attachment identity/lifecycle and product fit. Overlaps are
noted rather than repeated.

---

## 0. What I could not verify, and why

Stated first, because a sweep that hides its gaps is worth less than one that
does not.

| Not verified | Why |
|---|---|
| That `make docs-inventory` still reports 0 gaps after the OpenAPI fixes in §1.3 | The generator writes into `docs/prd/reports/`, i.e. it mutates tracked files. Running it would have put generated churn into another agent's working tree. `go run ./scripts/docs-surface-check` **was** run and is green, including the new page this sweep adds. |
| Runtime behaviour of the 12-agent fan-out against a real workspace | The dev clone's workspace has nowhere near thirteen live agents, and provisioning twelve to prove it would be a write. The finding is read off the source and the render path, both quoted below; it is a code reading, not an observation. |
| Whether the 400-agent search cap has ever been hit | No workspace anywhere near it. The cap's *behaviour* is settled from the SQL; its *incidence* is unknown. |
| Anything in `hooks/use-chat.ts`, `components/features/chat/assistant-turn.tsx`, `internal/manifest/**`, `internal/api/pipeline_trust.go`, `internal/journal/types.go` beyond a read | Uncommitted, actively being written by other agents. Findings that touch them are marked **in flight** and are not defect reports. |

---

## 1. The contract triple

House rule (`CLAUDE.md`): *every API endpoint gets a CLI command, and its
acceptance test drives the CLI binary.* Docs live under `docs/api-reference/`
and `docs/cli/`. `cmd/crewship/cli_route_contract_test.go` enforces the
route-exists half statically.

### 1.1 The table

| # | Route / surface | Route | CLI command | API doc | CLI doc | OpenAPI | Verdict |
|---|---|---|---|---|---|---|---|
| 1 | `PATCH /api/v1/agents/{agentId}/chats/{chatId}` (rename) | ✅ `internal/api/router_crews.go:345` | ✅ `crewship chat rename` | ✅ `api-reference/agents.mdx` → *Rename Chat* | ✅ `cli/chat.mdx` | ✅ `patch` present | **Complete** |
| 2 | `POST /api/v1/conversations/search` — workspace scope (`agent_id` now optional) | ✅ `internal/api/router_orchestration.go:831` (pre-existing) | ✅ `crewship conversation search` (positional → `--agent`) | ✅ `api-reference/conversations.mdx` | ✅ `cli/conversation.mdx` + `guides/conversation-search.mdx` | ✅ `agent_slug` / `agent_name` / `scope` in the response schema | **Complete** |
| 3 | `agents.suggested_prompts` on `GET /agents`, `GET /agents/{id}`, `PATCH /agents/{id}` | ✅ (fields on existing routes) | ✅ `crewship agent update --suggested-prompts`, read back by `agent get` | ✅ `api-reference/agents.mdx` | ✅ `cli/agent.mdx` | ❌ **absent from `Agent` and `CoreAgentUpdateRequestV2`** | **Gap — §1.3** |
| 4 | `agents.ask_forms` on the same three routes | ✅ | ✅ `agent update --ask-forms`, `agent ask-preview` | ✅ `api-reference/agents.mdx` | ✅ `cli/agent.mdx` | ❌ **absent from both schemas** | **Gap — §1.3** |
| 5 | `POST /api/v1/agents/{agentId}/chats/{chatId}/attachments` — changed error shape, new `409` | ✅ `internal/api/router_orchestration.go:738` (pre-existing) | ✅ `crewship chat attach` | ✅ `api-reference/agents.mdx` → *Chat Attachments* | ✅ `cli/chat.mdx` | ✅ | **Complete, one omission — §1.4** |
| 6 | `POST /api/v1/agents` (create) silently drops `suggested_prompts` / `ask_forms` | ✅ | ✅ `agent create` (no flags, correctly) | ❌ **nothing says the keys are ignored** | n/a | ❌ `CoreAgentCreateRequestV2` omits them, correctly | **Gap — §1.5** |
| 7 | Manifest `agent.ask_forms` (`crewship plan` / `apply` / `export`) | n/a (manifest field) | ✅ `crewship apply` / `export` | ❌ **`configuration/manifest-schema.mdx` documents `suggested_prompts` only** | ❌ | n/a | **Gap — §1.6, in flight** |
| 8 | `GET /api/v1/agents/{agentId}/chats/{chatId}/attachments` and `DELETE …/attachments/{attachmentId}` | ⚠️ **landing as this was written** — `internal/api/router_orchestration.go` (uncommitted) | ❌ none yet | ❌ | ❌ | ❌ | **In flight — §1.7** |

Nothing on this branch calls a route the router does not register, and nothing
clears its workspace before a workspace-gated route: both invariants in
`cli_route_contract_test.go` hold for the new commands. Note its own stated
coverage limit (call sites via the `getJSON`/`postJSON` wrappers are not seen)
— `chat rename` deliberately calls `client.Patch` directly so that it *is* seen
(`cmd/crewship/cmd_chat.go:511-515` explains why).

### 1.2 One soft finding on the acceptance-test half of the rule

`crewship chat rename`'s acceptance test drives the cobra `RunE` in-process
against an `httptest` server (`cmd/crewship/cmd_chat_rename_test.go:69`), not
the built binary. Its own header says it follows `cmd_chat_read_test`, and it
does — the whole `chat` tree is tested that way. But the two other commands
this branch added *do* exec the binary
(`cmd_conversation_workspace_test.go:78`, `cmd_agent_ask_test.go:223`), so the
rename is the odd one out on a branch that otherwise met the rule literally.

Not a defect and not worth a rewrite on its own. Worth one line in the PR
saying which standard it met, so nobody later reads the inconsistency as an
oversight.

### 1.3 Gap — `suggested_prompts` and `ask_forms` are absent from the OpenAPI contract

**Verified.** `internal/api/openapi.gen.json` contains zero occurrences of
either key, while the Go handler emits both unconditionally (non-`omitempty`
pointers, `internal/api/agents.go:304` and `:312`) and accepts both on PATCH
(`internal/api/agents_update.go:68-69`). So every `GET /api/v1/agents`
response currently carries two fields the published contract does not
describe, and a generated client would drop them.

The generator is hand-listed, so this is two one-line edits:

**(a)** `cmd/gen-openapi/schemas_core.go`, in the `agent := object(...)` literal
that begins at line 93 — add to the property map:

```go
"suggested_prompts": nullableString(), "ask_forms": nullableString(),
```

**(b)** `cmd/gen-openapi/schemas_request_core_resources_v2.go`, in
`request("CoreAgentUpdateRequestV2", ...)` at line 65 — add:

```go
"suggested_prompts": nullable(str()), "ask_forms": nullable(str()),
```

Then `make docs-inventory` (which runs `go run ./cmd/gen-openapi` first).

Do **not** add them to `CoreAgentCreateRequestV2`: `POST /api/v1/agents` genuinely
does not model them (see §1.5), and a schema that promises otherwise would be
the more expensive lie.

### 1.4 Omission — the 25 MB cap is missing from the API reference

`docs/api-reference/agents.mdx` → **Chat Attachments** documents the auth, the
multipart field, the container write, the `409`, the `413` and the `503`, but
never states the size cap. It is stated in `guides/chat-sessions.mdx` and
`cli/chat.mdx`, neither of which is where an API caller looks.

Exact text to add, after the `**Request:**` line in that section:

```markdown
The body is capped at **25 MB** (`internal/api/proxy_attachments.go`). Over
the cap the endpoint answers `400` with `invalid multipart form or file too
large (max 25MB)`; the console rejects the file before uploading and names it
in a toast.
```

### 1.5 Gap — `POST /api/v1/agents` silently drops both new keys

**Verified.** `createAgentRequest` (`internal/api/agents_create.go:15-36`)
models neither field, and `readJSON` (`internal/api/helpers.go:375-381`) is a
plain `json.Unmarshal` with no `DisallowUnknownFields`. A caller who POSTs
`{"name":…,"slug":…,"suggested_prompts":"…"}` gets `201` and an agent with the
column `NULL`, with nothing to indicate the key was ignored.

This is *why* `buildAgentPostCreateBody` exists in the manifest planner — the
apply path already compensates with a follow-up PATCH
(`internal/manifest/plan.go:1205-1215`, which names itself as the hook for
exactly this). So the behaviour is understood internally and undocumented
externally.

Recommended: document rather than change. Widening `Create` is a real API
change with a validation story of its own, and the PATCH follow-up already
works. Exact text to add to `docs/api-reference/agents.mdx`, under the **Create
Agent** request-body table:

```markdown
<Note>
`suggested_prompts` and `ask_forms` are **not** accepted here. Create ignores
unknown keys, so sending them succeeds with the columns left `null` — set them
with a follow-up [Update Agent](#update-agent) call, which is what `crewship
apply` does (`internal/manifest/plan.go`, `buildAgentPostCreateBody`).
</Note>
```

### 1.6 Gap — the manifest's `ask_forms` field is undocumented (in flight)

`internal/manifest/{schema,export,plan,validate}.go` gained `ask_forms`
alongside `suggested_prompts` — this is WP-3 and it is **being written right
now** in another agent's working tree, so it is reported as incomplete-in-flight
rather than as a defect. What is missing is the doc row;
`docs/configuration/manifest-schema.mdx` documents `suggested_prompts` (line
188) and its two validator rules (lines 258-259) but not `ask_forms`.

Exact text for whoever lands WP-3 — a row after the `suggested_prompts` row in
the agent field table:

```markdown
| `ask_forms` | string | — | The agent's questionnaires, as the canonical JSON array the server stores. A string, not nested YAML: export and apply must round-trip the server-normalised document byte-for-byte. At most 4 forms, 6 fields each; validated offline by `crewship plan` against the same `internal/askforms` contract the API uses, so a form `plan` accepts cannot later be refused by the PATCH. Omit it to leave the server's value alone. |
```

and a row in the cross-check table:

```markdown
| `ask_forms` parses and satisfies the ask-form contract | `<agent>: <the askforms error, naming the form and the placeholder>` |
```

**Also worth fixing while there:** the existing `suggested_prompts` row reads
"Omit it to leave the server's value alone **and fall back to the role packs**".
The second half is wrong — omitting leaves whatever the server already has,
which may well be a configured list. The fallback to role packs happens when the
column is `null`, which a manifest cannot cause (empty means *not declared*,
see `agentBodyDiffers` in `internal/manifest/plan.go:1132`).

### 1.7 In flight — two chat-attachment routes landed mid-sweep

Between the start of this sweep and its last read,
`internal/api/router_orchestration.go` gained two registrations in another
agent's working tree:

```go
r.mux.Handle("GET /api/v1/agents/{agentId}/chats/{chatId}/attachments", authed(wsCtx(http.HandlerFunc(proxy.ListAgentChatAttachments))))
r.authedMut("DELETE", "/api/v1/agents/{agentId}/chats/{chatId}/attachments/{attachmentId}", roleCreate, proxy.DeleteAgentChatAttachment)
```

This is WP-8(3), and it is **not reported as a gap** — it is unfinished work
being finished. What it will owe on landing, so it is not discovered later:

- **CLI:** `crewship chat attachments list <chat-id>` and
  `crewship chat attachments remove <chat-id> <attachment-id>`, under the
  existing `chat` tree in `cmd/crewship/cmd_chat.go`, resolving the agent with
  the same `lookupChatAgentID` the other subcommands use. Note that the
  route-contract test only sees call sites shaped as a method on `*cli.Client`
  — use `client.Get` / `client.Delete`, not the `getJSON` / `deleteJSON`
  wrappers, or the commands drop silently out of the invariant.
- **API docs:** two sections in `docs/api-reference/agents.mdx` beside *Chat
  Attachments*, and two rows in the route table at the top of that file; the
  same two rows in `docs/api-reference/conversations.mdx`'s table.
- **CLI docs:** two rows in the subcommand table in `docs/cli/chat.mdx`.
- **OpenAPI:** both operations in `cmd/gen-openapi`, then `make docs-inventory`.
- **Three doc statements go stale the moment this merges**, each currently
  asserting that no such endpoint exists:
  `docs/guides/chat-sessions.mdx` (twice — the **Attachment** glossary row and
  the **Composer** section), and `docs/api-reference/agents.mdx` if it repeats
  it. `docs/guides/chat-surface-limits.mdx` was written to avoid the claim.
- **The reclaim question reopens.** `internal/api/attachments_gc.go:60-61`
  excludes chat blobs from the sweeper *because* nothing could delete them.
  With a delete endpoint that rationale no longer holds on its own terms; the
  exclusion may still be right, but the comment now needs to say why for a
  different reason. `internal/api/attachments_gc.go` is also modified in the
  working tree, so this may already be in hand.

---

## 2. Silent caps

The PRDs' own rule is *no silent caps — log what was dropped.* Measured against
that rule rather than against whether a number appears somewhere.

### 2.1 The table

| Cap | Value | Enforced at | Behaviour at the boundary | Documented? | Logged? | Visible on screen? |
|---|---|---|---|---|---|---|
| Agents given a thread fan-out | 12 | `chat-tree-sidebar.tsx:212` (`AGENT_FANOUT_CAP`), applied at `:299` | Agent still gets a row; its threads are never fetched and render as **count `0`, no chevron** | **No** → now yes, `guides/chat-surface-limits.mdx` | **No** | **No — and indistinguishable from "no history"** |
| Threads per agent in that fan-out | 10 | `chat-tree-sidebar.tsx:215`, sent as `?limit=` | Older threads absent from the merged list | Now yes | No | No |
| Threads on the `/chat` index | 25 | `chat-tree-sidebar.tsx:218`, applied in `mergeRecentThreads` (`chat-home.tsx:130`) | List cut to the 25 most recent | Now yes | No | Partly — the heading counts what is shown |
| Agents in a workspace search | 400 | `internal/api/conversation_search.go:66`, SQL `LIMIT` at `:108` | Newest 400 searched, rest omitted | **Yes** — `api-reference/conversations.mdx`, `guides/conversation-search.mdx` (both added on this branch) | **No** | No — `scope` still reads `workspace` |
| Chat attachment size | 25 MB | `internal/api/proxy_attachments.go:74`; browser at `attachment-zone.tsx:20` | `400`; browser toast names the file | Yes (guides + CLI; **not** the API ref — §1.4) | n/a | **Yes** |
| Suggested prompts / prompt length | 8 / 120 chars | `internal/api/agents_suggested_prompts.go:25,29` | `400` naming the prompt by position | Yes | n/a | **Yes** |
| Ask forms / fields / label / template | 4 / 6 / 48 / 2000 | `internal/askforms/forms.go:43,47,51,53` | `400` naming the form and the placeholder; nothing written | Yes | n/a | **Yes** |
| One rendered answer / whole rendered message | 2000 / 32000 chars | `internal/askforms/render.go:45,50` | **Silently truncated at send** | Yes (`api-reference/agents.mdx`) | No | **No** |
| Session title | 200 chars | `internal/api/agent_chats_rename.go:38` | `400`, never truncation | Yes | n/a | **Yes** |
| Inbound WS frame | 64 KiB | `internal/ws/hub.go:166` | Send refused; draft and attachments survive | Yes | Yes | **Yes** |

### 2.2 The brief's premise, corrected

The brief said none of these was documented anywhere a user or operator would
find it. That is **true for the 12/10/25 fan-out** and **false for the other
three**:

- the **400-agent** cap is documented twice, both added on this branch —
  `docs/api-reference/conversations.mdx` ("Workspace scope spans at most 400
  agents (the most recently created ones); the bound exists because the agent
  ids are bound query parameters") and `docs/guides/conversation-search.mdx`;
- the **25 MB** cap is in `docs/guides/chat-sessions.mdx` and
  `docs/cli/chat.mdx` (both predate this branch for the guide, updated on it
  for the CLI page). Its only hole is the API reference — §1.4;
- the **8 / 4 / 6** caps are documented at length in
  `docs/api-reference/agents.mdx` and `docs/cli/agent.mdx`, and none of them is
  silent in the first place: each is a `400` that names the offending prompt,
  form or placeholder, and writes nothing.

Confirmations are as useful as corrections. The documentation debt here is one
cap wide, not four.

### 2.3 What the thirteenth agent's owner experiences

Traced through the render path, because "loses threads" understates it.

1. `useChatTreeData` fetches the roster, filters retired agents, and slices to
   twelve: `const scope = agents.filter((a) => a.slug !== skipSlug).slice(0,
   AGENT_FANOUT_CAP)` — `chat-tree-sidebar.tsx:299`.
2. Agents past the slice never enter `threadsByAgent` **and never enter
   `threadErrors`**. The tree already has a first-class representation of *"this
   list is unknown"* — a failed fetch yields an em dash instead of a count
   (`chat-tree-sidebar.tsx:1001`, `{threadsError ? "—" : totalThreads}`) plus
   an ungated `ScopeFailure` panel reading `this is not an empty history; the
   list could not be fetched` with a **Retry**. The capped agent gets neither.
3. `totalThreads` is therefore `0`, so `canExpand` is `false`
   (`chat-tree-sidebar.tsx:902`) — no chevron, no keyboard disclosure.
4. Narrow the tree to that agent and the branch renders **Start a
   conversation** (`chat-tree-sidebar.tsx:1086-1099`), whose own comment
   describes it as the row for "an agent nobody has talked to".
5. On `/chat`, `mergeRecentThreads` flat-maps over every agent but only twelve
   have entries (`chat-home.tsx:151`), and the sub-bar reads
   `${threads.length} recent · ${agents.length} agents` — so a workspace of
   forty agents reads "25 recent · 40 agents" with nothing to say the 25 came
   from twelve of them.

So: an agent with a year of history is presented as an agent with none, in the
product's primary navigation, by the same failure shape (`"we could not ask"`
rendered as `"there is nothing"`) that WP-1 names as the house defect and that
this very file already fixed once for failed fetches.

### 2.4 Which caps need a screen affordance rather than a doc line

**Needs UI. Nothing else will do.**

- **The 12-agent fan-out.** A doc line cannot reach the person looking at a
  wrong `0`. The cheapest honest fix reuses machinery that already exists in
  this file: when `agents.length > AGENT_FANOUT_CAP`, populate `threadErrors`
  for the agents past the slice with a *reason* rather than an error — e.g.
  `"not loaded on this view"` — so the row renders the em dash instead of `0`,
  and give `ScopeFailure` a non-error variant reading `<agent>'s conversations
  are not loaded here — open the agent to see them` with **Open** where the
  Retry sits. That is a distinguishable state, which is the standard WP-1's
  acceptance criterion asks for. A footer on `/chat` — `Showing recent threads
  from 12 of 40 agents` — covers the index.

**Needs a log line, not UI.**

- **The 400-agent search cap.** A user cannot act on it and should not be told;
  an operator diagnosing "why did search not find it" absolutely should.
  `workspaceSearchAgents` (`internal/api/conversation_search.go:104`) knows both
  numbers already — a second `COUNT(*)` or a `LIMIT 401` probe, and one
  `logger.Warn("conversation search scope truncated", "workspace_id", …,
  "searched", 400, "total", n)`. Optionally a `scope_truncated: true` alongside
  `scope` in the response, which the CLI could print as `searched the 400
  newest agents of 612`.

**Doc line is sufficient.**

- 10 threads per agent and 25 on the index — bounded lists on a surface whose
  job is recency; the full list is one click away on the agent's own page.
- The 2000 / 32000 render truncations — reachable only by a very long
  `textarea` answer, and a character counter on the field would be a nicer fix
  than a doc line but is not urgent. Both renderers agree (Go and TS are pinned
  to `testdata/ask-templates.json`), and `crewship agent ask-preview` shows the
  truncated result exactly, so an author can see it before a user does.

**Already correct.** The 25 MB cap, the ask-form and suggested-prompt caps, the
200-character title cap and the 64 KiB frame cap all refuse loudly and name
what was wrong. They are the standard the fan-out should be held to.

### 2.5 Where the documentation went

`docs/guides/chat-surface-limits.mdx` (new, this sweep), registered in
`docs/docs.json` next to `guides/chat-sessions` so it is reachable — an
unregistered page is the `pins.md` failure mode. `go run
./scripts/docs-surface-check` passes with it in place: 271/271 stability
labels, 1451 prose links, 0 dead.

---

## 3. Claim check

Three claims were already known false (`conversation search is unwired`,
`pins.md is unreachable`, `CREW.md has no writer`) and are not re-litigated.
Everything below is a fresh check of the remaining load-bearing claims, each
opened rather than trusted.

Three verdicts are used, and the distinction matters:

- **FALSE** — was not true when written.
- **STALE** — was true when written and the work has since landed. The claim
  needs deleting, not correcting.
- **CONFIRMED** — still true, at the cited location or a moved one.

### 3.1 Corrections — claims that are FALSE

| # | Claim | Where | Evidence | Correction |
|---|---|---|---|---|
| C1 | "approval rules that actually match chat runs (`internal/policy/approval_mode.go:25-30`)" — cited as the unlock condition for the inline decision card | chat PRD §6.1 | `approval_mode.go:9-11` defines three *modes* (`none`/`async`/`sync`) and nothing else; lines 25-30 are a **comment**, not matching logic. Chat runs do carry a mode (`internal/api/agent_config.go:377` → `internal/chatbridge/resolver.go:682` → `agentrun.go:99`) and are gated with `Tool: "agent_run"` (`internal/orchestrator/orchestrator_run.go:155-166`), but **no default rule can match them**: `internal/harbormaster/rules.go:52` keys on `(?i)deploy\|production\|delete_.*\|drop_.*\|terminate_.*`, and the other two defaults key on `cost_estimate_usd` and `target`/`host`/`env` args — none of which chat runs pass (`orchestrator_run.go:160-164` passes only `agent_slug`, `agent_role`, `user_prompt`). | The unlock condition is not "rules that match" but "a rule *shape* that can match an agent run at all". Cite `harbormaster/rules.go:52` as the thing that must change, not `approval_mode.go`. |
| C2 | "`internal/api/pipeline_trust.go:139-157` grants and revokes with no journal entry" — line range | work order WP-4(a), chat PRD §6.1 | The claim's *substance* is correct: the file has zero journal references. But the handlers are `GrantTrust` at `:67` ending in `broadcastInboxUpdated(workspaceID, "trust_granted")` at `:151`, and `RevokeTrust` at `:203` ending at `:251`. The range `139-157` covers only the grant tail. | Cite `pipeline_trust.go:151` and `:251`. **In flight** — `pipeline_trust.go`, `internal/journal/types.go` and a new `pipeline_trust_journal_test.go` are all modified in the working tree as of this writing, so WP-4(a) is being fixed now. |
| C3 | "`internal/sidecar/query.go:339-366`" vs "`internal/sidecar/query.go:326`" — the two documents cite different lines for the same 300 s give-up | chat PRD §6.1 vs work order WP-4(b) | **Both are right about different things.** The timeout itself is `query.go:326` — `context.WithTimeout(r.Context(), 300*time.Second)` (client safety net at 310 s, `:19-24`). What happens after is `:339-347`, `:351-358` and `:361-368`, three branches that all return **HTTP 200 with `"status": "TIMEOUT"`** and an empty resolution. | Not a correction so much as a merge: cite `query.go:326` for the deadline and `:339-368` for the three silent-success exits. The severity is worse than either document states — the agent's tool call *succeeds*, so nothing downstream can distinguish "answered" from "gave up". |
| C4 | "`internal/api/pipelines_crud.go:547` — add `author_chat_id` to `userSaveRequest`; the store already writes the column and the sidecar already stamps it" | chat PRD §6.2 | The instruction is sound and the line is inside `userSaveRequest` (declared `:541`). But the field already exists **on the other struct** — `internalSaveRequest` (`:519`) carries `AuthorChatID string \`json:"author_chat_id"\`` at `:527`, and `userSaveRequest`'s own doc comment (`:541-546`) says authorship is inferred from the JWT *on purpose*. | Say what the change actually is: not "add a missing field" but "decide that a user-authored routine may declare its originating chat", which contradicts a documented design choice. That is an owner decision, not a patch. |
| C5 | "`chats.title` … every session in `crewship chat list` reads `-` forever" and adjacent framing | (background, both docs) | CONFIRMED as history, but note `docs/configuration/manifest-schema.mdx:188` now says omitting `suggested_prompts` makes an agent "fall back to the role packs". It does not — omission leaves the server value alone; the fallback needs `null`. | See §1.6. |

### 3.2 Stale — true when written, fixed since

These need **deleting** from the PRDs, not correcting. Leaving a fixed defect
described as open is how the next reader stops trusting the document.

| # | Claim | Where | Evidence it landed |
|---|---|---|---|
| S1 | "`SlashActionModal` is imported by nothing" | chat PRD §6.2 | True on `main` (`slash-palette.tsx:53` mentions it in a comment only). Now imported at `chat-panel.tsx:34` and rendered at `:850`. Landed in `5725bcb8`. |
| S2 | "`chat-panel.tsx:589` passes no `workspaceId` so no server action ever appears" | chat PRD §6.2 | True when written — the modal was not imported or rendered in `chat-panel.tsx` at `736126b4`, the commit that added the PRD. Now `chat-panel.tsx:849-855` **gates** the modal on it: `{workspaceId && (<SlashActionModal … workspaceId={workspaceId} …>)}`, with `workspaceId` from `useWorkspace()` at `:123`. Line 589 today is inside an unrelated `useMemo`. |
| S3 | "the conversation-search endpoint requires `agent_id` … **Decide before starting** (O2)" | chat PRD §4 Step 6 | O2 is decided and built: `agent_id` is optional (`conversation_search.go:159-174`), workspace scope resolves agents from the request context, and a searcher that cannot span agents answers an honest `503` rather than narrowing silently (`:240-246`). Step 6's "Do" paragraph and O2 in §8 both still pose it as an open question. |
| S4 | Step 1's whole work list — dead `/crews/agents/*` links, the orphaned page clients, `overview-tab.tsx` missing `?session=` | chat PRD §4 Step 1 | All done. Onboarding lands at `/chat/<slug>` (`app/(onboarding)/onboarding/page.tsx:484` — note the PRD's path is wrong, the file is under `(onboarding)`, not `(dashboard)`); `welcome-checklist.tsx:104` likewise; `overview-tab.tsx:346` carries `?session=`; `components/features/agents/` retains only `agent-card.tsx` and `agent-learning-toggle.tsx`; a regression test pins the absence (`app/(onboarding)/onboarding/__tests__/dead-agent-routes.test.ts:195`). |
| S5 | "`AskUserCard` renders a lie" | work order WP-5, chat PRD §6.1 | Resolved and §6.1 already records it. `assistant-turn.tsx` is **in flight** (uncommitted) — read but not assessed. |
| S6 | "Steps 1–7 add no endpoints" | chat PRD §9 | Already corrected by another agent in the working copy; §9 now reads "Steps 1–7 added one endpoint, `PATCH /api/v1/agents/{agentId}/chats/{chatId}`". Confirmed against `router_crews.go:345` and `testdata/route-roles.txt`. |

### 3.3 Confirmed — still true, and worth keeping

| # | Claim | Evidence |
|---|---|---|
| K1 | Escalations never expire; there is no `EXPIRED` state and no sweeper (WP-4b) | The only `UPDATE escalations SET status` sites are the human resolve (`internal/api/escalation_handler.go:512`, guarded `AND status = 'PENDING'`) and the credential auto-resolve (`internal/api/escalation_autoresolve.go:154`). Nothing else, no background job. **Adjacent finding not in either document:** `internal/api/agent_inbox.go:95` counts `status IN ('pending','open')` in lowercase while rows are written `'PENDING'` (`escalation_handler.go:177-179`) — on SQLite's case-sensitive comparison that count is always 0. |
| K2 | Escalations already carry `chat_id` and already broadcast on the session channel | `escalation_handler.go:272-277` `broadcastChannelEvent(h.hub, "session", body.ChatID, "escalation_created", …)`; workspace fan-out `:278-284`; resolve side `:633`; journal refs `:268`. |
| K3 | The blocking escalation protocol parks and resumes correctly | `escalation_waiter.go:93` handler; register `:134-135`; re-read closing the lost-wakeup window `:139`; the park itself `:171-184`; wakeups via non-blocking `notifyEscalationWaiter` `:39-51`. |
| K4 | `AfterDecide` is the shape a trust-grant journal entry should copy | `internal/harbormaster/store_mutate.go:222`, emit at `:227-250` (`EntryApprovalGranted` / `EntryApprovalDenied`), reward history `:259-269`. The claimed 222-277 range is accurate. |
| K5 | `AskUserQuestion` is a harness builtin with no Crewship backing, and a test asserts its absence from every profile | `internal/orchestrator/tool_profiles.go:12`; `tool_profiles_test.go:23` in the `forbidden` list, asserted across `MINIMAL`/`CODING`/`FULL`/`""`/`BOGUS` at `:27` and `:42-46`. |
| K6 | `POST /api/v1/memory/write` does not exist, on purpose | No such registered route; `internal/api/slash_commands_handler.go:84-92` explains it, naming the sidecar loopback `/memory/write` (`internal/sidecar/server.go:433`) as the only writer. |
| K7 | The HITL verifier is written and entirely unwired | `internal/memory/verifier.go:44` says so itself. `VerifyWrite` (`:157`) has one non-test caller, `internal/memory/writer.go:155`, guarded by `Verifier.Mode != VerifierOff` at `:154` — and no shipping config sets `Verifier:` at all (the two `WriteConfig{}` literals are `internal/sidecar/memory_write.go:160-164` and `internal/api/memory_portability.go:434-437`, neither sets it). Reachable by construction, unreachable in practice. |
| K8 | The indexer discards chunk line ranges, so citations cannot carry `file:line` | `internal/memory/index.go:121-123` binds only `chunk.File` and `chunk.Content`; `Chunk` carries `LineStart`/`LineEnd` (`chunk.go:10-15`, populated at `:63-64,83,101`). Computed and thrown away. |
| K9 | `GET /chats/{id}/messages` answers 200 with an empty list for an unknown chat (the trap Step 3 hit, and the "Do not" in work order §5) | `internal/api/proxy.go:263` handler; decision at `:278-282`. The read-tier role gate runs first (`:268-271`), so a non-reader still gets 403 — the endpoint is not an existence oracle *or* a permission oracle. |
| K10 | `isSessionOwner` refuses a session channel when no `chats` row exists | `internal/ws/channel_auth.go:161-169`; `existsRow` (`:109-117`) maps `sql.ErrNoRows` to a definitive deny, distinct from a transport error. |
| K11 | The static export rewrites exactly one path level | `internal/api/static.go:203-217`; `parts[:len(parts)-1]` with no ancestor loop, and the comment naming the `/chat/a/b` misroute it was written to stop. Confirms work order §5's "do not introduce a deeper chat route". |
| K12 | The agent slug is parsed from `window.location.pathname` | `chat-page-client.tsx:66` in `useAgentSlugFromUrl` (`:60`), with the static-export rationale at `:45-48` and a `popstate` re-read at `:70`. |
| K13 | `getSuggestions` prefers the agent's list and falls back to the role packs | `lib/agent-suggestions.ts:85`, own-list preference at `:93-95`. **Nuance worth adding to the PRD:** the override is partial — only the `empty`-state chips come from the agent; `followUps` always come from the role pack (`:80-84`). Step 7 reads as though the whole pack is replaced. |
| K14 | Schedule create requires an existing target routine | `internal/api/pipeline_schedules.go:226` → `resolveSchedulePipelineID` at `:560-579`, four distinct 400s. |
| K15 | The sidecar stamps `author_chat_id` and `save_routine` exists | `internal/sidecar/pipelines.go:174`; `internal/sidecar/routine_mcp.go:92` (descriptor) and `:249` (dispatch). |
| K16 | `chats.agent_id` is single-valued and `NOT NULL`; `chat_participants` knows only `user_id` | `internal/database/migrate_consts_v01_init.go:163`; `migrate_consts_v118_group_chat.go:19-25`. §7's two blockers are exactly right. |
| K17 | `POST /chats/{id}/participants` has no frontend caller | The only reference outside the router is the OpenAPI table (`cmd/gen-openapi/schemas_public_activity.go:115`). The frontend only *reads* participants (`chat-panel.tsx:315`, a GET used to build a display-name map). Group chats can be rendered but not composed. |
| K18 | Chat notifications deep-link to `/chat/<slug>?session=<id>` | `internal/chatnotify/notify.go:155`, carried as `Payload["chat_url"]` at `:186`. **Minor asymmetry worth a line:** neither segment is URL-escaped here, unlike every frontend caller which uses `encodeURIComponent`. Slugs and ids are constrained enough that it is benign today. |
| K19 | §10's shipped constraints | 900 px compact breakpoint: `chat-tree-sidebar.tsx:170` (`CHAT_TREE_BREAKPOINT = 900`). `/chat` mounts no panel: `chat-home.tsx` renders only the sub-bar, the tree, recent threads and the agent list — no `ChatPanel`, hence no chat socket. Attachments require a running crew: confirmed by the `409` path. |
| K20 | `truncateRunes` really does count characters, in both languages | `internal/askforms/render.go:294-304` and `lib/ask-template.ts:286-290`. The `len(s) <= max` first branch is a **fast path**, not a byte cap: a byte length under the cap can never be more runes than the cap. Checked because the naming invites the opposite conclusion, and the codebase treats byte-vs-character caps as a defect class. |
| K21 | WP-2's telemetry claim | `grep ask_chip_` across the tree returns only the two PRD/work-order mentions themselves. No instrumentation shipped. |
| K22 | WP-7's test-integrity claim | `playwright.config.ts:13-29` — eight specs in `testIgnore`, `onboarding-wizard.spec.ts` among them with a comment routing it to `playwright.fresh.config.ts` in the e2e-devcontainer **nightly**. The flagship repair of this branch does not run on a PR. |
| K23 | WP-8(2) — the composer persists drafts but not attachments | `stores/composer-store.ts:209` — `partialize: (s) => ({ modelId: s.modelId, drafts: s.drafts })`. The `File` handle is explicitly never serialised (`:32-35`). |
| K24 | WP-8(3) — no listing or delete endpoint for chat attachments, deliberately outside the reclaim machinery | Was true at the start of this sweep (no `owner_type = 'chat'` read path in `internal/api`; `attachments_gc.go:60-61` states the exclusion). **Superseded mid-sweep** — see §1.7. |

### 3.4 One finding outside every claim

The `/crews/agents/*` **sub-route** cleanup is complete (S4). The bare **index**
route `/crews/agents` is not, and it does not exist:

- `components/layout/app-toolbar.tsx:209` and `:236` — a `<Link>` whose href is `/crews/agents`
- `app/(dashboard)/agents/page.tsx:9` — `router.replace("/crews/agents")`, plus
  a meta-refresh at `:13` and an `<a href>` at `:14`

There is no `app/(dashboard)/crews/agents/page.tsx`, and the static export has
no `web/out/crews/agents.html` — these three call sites land on the SPA
fallback. Same defect class as Step 1, one level up, and not named in either
PRD. Not fixed here (`.tsx`, and outside this sweep's remit); the fix is
presumably `/crews` for the toolbar and a redirect target change in
`app/(dashboard)/agents/page.tsx`.

---

## 4. Summary of what to apply

Nothing below was applied by this sweep except the two doc items marked ✅.

| Item | File | Owner |
|---|---|---|
| ✅ Limits page, written and registered | `docs/guides/chat-surface-limits.mdx`, `docs/docs.json` | done here |
| `suggested_prompts` / `ask_forms` in the OpenAPI agent schema | `cmd/gen-openapi/schemas_core.go:93`, `schemas_request_core_resources_v2.go:65` | whoever owns `internal/api` |
| 25 MB cap in the API reference | `docs/api-reference/agents.mdx` → Chat Attachments | free |
| Create-ignores-these-keys note | `docs/api-reference/agents.mdx` → Create Agent | free |
| Manifest `ask_forms` doc row + the `suggested_prompts` wording fix | `docs/configuration/manifest-schema.mdx` | the WP-3 agent (in flight) |
| Fan-out cap → a distinguishable state in the tree | `components/features/chat/chat-tree-sidebar.tsx` | frontend |
| Search-scope-truncated log line | `internal/api/conversation_search.go` | backend |
| PRD deletions S1–S4 and corrections C1–C5 | `docs/prd/chat-as-a-primary-surface.md` | the PRD agent (in flight — **not edited here**) |
| `/crews/agents` index links | `components/layout/app-toolbar.tsx`, `app/(dashboard)/agents/page.tsx` | frontend |
| CLI + docs + OpenAPI for the two attachment routes landing now | see §1.7 | the WP-8 agent (in flight) |
