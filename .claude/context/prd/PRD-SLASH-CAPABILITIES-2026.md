# PRD: Slash Commands + Per-User Capabilities (May 2026)

| Field | Value |
|---|---|
| Owner | Pavel |
| Status | draft — pending approval |
| Scope | enterprise multi-tenant agent gate: end-user slash commands in chat UI + CLI, gated by per-user capabilities assigned by workspace admins |
| Related | [MEMORY-ROADMAP-2026.md](MEMORY-ROADMAP-2026.md), [PRD-AGENT-EVOLUTION-2026.md](PRD-AGENT-EVOLUTION-2026.md) |

## 1. Context

Today the in-chat agent surface and the CLI surface have diverged.
CLI (`cmd/crewship/*`) exposes the full MANAGER-grade product —
`routine schedules create`, `skill create`, `credential
create/rotate`, `issue create/comment/...` — because CLI talks to the
public API with the user's JWT and `requireRole` gates do the right
thing. Chat-UI agent talks to the sidecar (`localhost:9119`), which
maintains its own whitelist of forwarded routes
(`internal/sidecar/server.go:309-456`). Three high-value routes never
got their sidecar mirror:

- routine creation (cron / pipeline-schedules)
- skill creation (LLM-generated or imported)
- credential creation / rotation

So an agent in chat cannot do these even if it wants to, and a user
chatting with that agent can't ask "make a routine from this".

A separate but adjacent problem: when the chat-agent *can* reach the
backend (e.g. `/issue/create`), the sidecar injects `MANAGER` role
blanket-style (`internal/api/internal_hire.go:69-70`) and drops the
caller's user_id. Audit log records the action as `system`. In a
single-operator workspace that's fine. In an enterprise tenant
(Ludmila at Microsoft chatting alongside 500 other employees) it's
both a security gap (every chat-user effectively has MANAGER reach
into the four exposed routes) and an audit gap (no per-user
attribution).

This PRD closes both gaps in one PR by introducing a **slash-command
surface** as the end-user gate. End users see only the actions they
were explicitly granted; agents continue to act autonomously where
the crew's `autonomy_level` permits; the two enforcement paths stay
distinct in the handler layer.

## 2. Goals

- End users (MEMBER role) can perform high-value actions —
  routine/skill/credential/issue creation, memory write — only when an
  admin has explicitly granted the capability
- Chat UI and CLI render the same slash-command palette (server-driven
  list), so parity is structural, not aspirational
- Workspace admins can grant capabilities per user, per workspace,
  via a checkbox grid in the Members tab (no role-tier promotion needed)
- Slash-command actions carry user identity end-to-end (`user_id` in
  audit log, not `system`)
- Three missing sidecar routes (routine, skill, credential) land
  behind the same capability check, so autonomous agent calls and
  user-clicked slash actions share one enforcement layer
- Existing role-based gates continue to work; capabilities are
  layered *on top of* roles, not replacing them
- Zero regression for single-operator self-host installs — default
  capability bundle for OWNER/ADMIN covers everything they did before

## 3. Non-goals (explicit cuts)

- **No new role tier.** Capabilities replace the need for finer roles;
  we are not adding `EDITOR` between MEMBER and MANAGER.
- **No per-resource ACLs.** Capability `routine.create` lets the user
  create *any* routine in the workspace, not "this specific routine".
  Per-resource ACL is a future iteration if SaaS demands it.
- **No SCIM auto-mapping in this PR.** Preset bundles ship; mapping
  to IdP groups deferred to a follow-up.
- **No marketplace of shareable slash commands.** Slash commands are
  defined by the platform + the user's `~/.crewship/commands/*.md`
  files (existing CLI surface). No upload/share UX.
- **No capability inheritance across workspaces.** Each
  `workspace_members` row carries its own capability set; users
  with multiple workspace memberships configure each one separately.
- **No revoke-on-running-action.** If admin removes
  `routine.create` while user has a routine creation modal open,
  the submit still succeeds. UX nuance deferred — backend re-checks
  capability at submit only.

## 4. Constraints

- Self-host runtime; one PostgreSQL/SQLite, no separate auth service
- Must work in both Postgres and SQLite (JSONB column with JSON in
  SQLite via JSON1 extension already in use)
- Migration must be online — existing single-operator installs
  cannot lose chat capability the moment they upgrade
- HTML wireframe required before React for the two new UI surfaces
  (Members capability grid + slash actions modals) — per project
  convention [[feedback-html-wireframe-first]]
- Audit trail for capability changes themselves (Pavel grants Ludmila
  `routine.create` → row in audit log, same shape as role changes)

## 5. Roadmap overview

```text
PR (single)  Slash commands + capabilities + sidecar parity         [proposed]
   commit 1  Migration: capabilities column + backfill              [foundation]
   commit 2  capabilities.go: requireCapabilityOrForbid             [enforcement]
   commit 3  IPC: propagate X-Caller-User-Id through sidecar        [identity]
   commit 4  Three internal API mirrors (routine/skill/credential)  [API parity]
   commit 5  Three sidecar routes proxying the above                [sidecar parity]
   commit 6  GET /slash-commands + handler with capability filter   [slash registry]
   commit 7  Frontend: slash palette actions + Members grid         [UI]
   commit 8  CLI: server-driven slash list + admin capability cmds  [CLI]
```

Eight commits, one PR. Earlier commits compile + pass tests
standalone; later commits depend on earlier. PR cannot be
half-merged.

## 6. Commit detail

### 6.1 Migration + capability constants

`ALTER TABLE workspace_members ADD COLUMN capabilities JSONB
DEFAULT '["chat"]'`. SQLite path uses a TEXT column with JSON
serialization (`internal/database/json_compat.go` pattern already in
use for other JSON-shaped columns).

Backfill query in the same migration:

```sql
UPDATE workspace_members
SET capabilities = CASE role
  WHEN 'OWNER'   THEN '["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]'
  WHEN 'ADMIN'   THEN '["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]'
  WHEN 'MANAGER' THEN '["chat","routine.create","issue.create","memory.write"]'
  WHEN 'MEMBER'  THEN '["chat"]'
  WHEN 'VIEWER'  THEN '["chat"]'
END
WHERE capabilities IS NULL;
```

Capability strings live as constants in `internal/api/capabilities.go`
so handler call sites typo-check at build time.

### 6.2 `capabilities.go` — enforcement helper

New file `internal/api/capabilities.go`:

```go
func requireCapabilityOrForbid(
    w http.ResponseWriter, logger Logger,
    callerUserID, workspaceID, capability, resource string,
) bool
```

Loads the caller's `capabilities` JSON from `workspace_members`
(cache TTL 30s for hot path), checks membership, audits on deny via
the same `replyForbidden` path role checks use (`rbac.go:251-263`).
Helper coexists with `requireRoleOrForbid` — handlers can call
either, both, or layer them. For routine/skill/credential handlers we
layer: first role gate (existing), then capability gate (new). Belt +
braces during the rollout.

### 6.3 IPC identity propagation

Originally drafted as a field on `IPCConfig`; revised on implementation
because IPCConfig is static-per-sidecar-lifetime (one config = one agent
container) but caller user id is per-request — Ludmila and Pavel can
chat with the same agent in different windows. Per-request propagation
through HTTP headers is the only correct shape.

Inbound chat-bridge / CLI repl request to the sidecar carries:

```
X-Caller-User-Id: <user id>     (omitted for autonomous-agent tool calls)
X-Caller-Source:  chat-ui | cli-repl
```

`proxyToAPIFiltered` (`internal/sidecar/coordinator.go:195`) is the
single chokepoint for sidecar → backend IPC; it propagates the two
headers verbatim when present, omits them when absent (so autonomous-
agent calls look identical to pre-PR behaviour on the wire).

Backend reads them via the new helper:

```go
// internal/api/caller_identity.go
func CallerUserIDFromRequest(r *http.Request) string
func CallerSourceFromRequest(r *http.Request) string
```

`CallerUserIDFromRequest` prefers `UserFromContext` (set by
AuthMiddleware on JWT / CLI-token paths) over the header so a JWT
caller can't spoof a different user's id by also setting the header.
The header path is the only signal on internal-token routes (no
AuthMiddleware there).

Empty return from `CallerUserIDFromRequest` is the discriminator the
dual-path handlers use to decide capability check (user-attributed)
vs. autonomy_level check (autonomous). No context key changes; no
middleware rewrite of `ctxRole` (deferred — the blanket-MANAGER
injection stays for now and the dual-path handlers in commit 6
ignore it when the caller-id header is present).

This commit, in isolation, closes the audit-attribution gap even
before any new capability check is wired — handlers that adopt
`CallerUserIDFromRequest` can already log user-attributed actions.

### 6.4 Three internal API mirrors

Pattern `internal/api/internal_hire.go:13-17`: each new file
exposes a thin adapter over the public handler with workspace context
injected from query params.

```
internal/api/internal_routines.go     → InternalRoutineHandler.CreateSchedule
internal/api/internal_skills.go       → InternalSkillHandler.Generate
internal/api/internal_credentials.go  → adds Create + Rotate (LIST exists)
```

Each calls the public handler after asserting the `ctxCallerUserID`
context has a non-empty user id (no anonymous calls — autonomous
agent goes through `autonomy_level` path instead, see §6.5).

Routes registered in `router_internal.go`:

```
POST /api/v1/internal/routines/schedules
POST /api/v1/internal/skills/generate
POST /api/v1/internal/credentials/{id}/rotate
```

(POST /api/v1/internal/credentials already exists for status patches;
add the body-create variant under the same route group.)

### 6.5 Three sidecar routes

Five-line handlers in the `internal/sidecar/` package, registered in
the switch at `server.go:309-456`. Pattern `spawn.go:43-55`.

```
POST /routines/schedules/create  → proxyToAPI("POST /api/v1/internal/routines/schedules")
POST /skills/generate            → proxyToAPI("POST /api/v1/internal/skills/generate")
POST /credentials/create         → proxyToAPI("POST /api/v1/internal/credentials")
POST /credentials/{id}/rotate    → proxyToAPI("POST /api/v1/internal/credentials/{id}/rotate")
```

Sidecar does **no** capability check itself. It just forwards. The
authoritative gate lives in the backend handler (one enforcement
layer, no drift risk).

Dual-path logic in each backend handler:

```go
caller := r.Context().Value(ctxCallerUserID).(string)
if caller != "" {
    // User-attributed: capability check
    if !requireCapabilityOrForbid(w, logger, caller, ws, "routine.create", res) {
        return
    }
} else {
    // Autonomous agent: autonomy_level check on the crew
    if !requireAutonomyOrForbid(w, logger, crewID, "routine.create", res) {
        return
    }
}
```

`requireAutonomyOrForbid` already exists in spirit at
`internal/policy/` — this commit formalizes its surface for the three
new actions.

### 6.6 `GET /api/v1/slash-commands`

New public handler `internal/api/slash_commands_handler.go`. Loads
the platform-defined slash command catalog (static Go map for now,
file-driven in a follow-up), intersects with the caller's
capabilities, returns a JSON array. Shape:

```json
[
  {
    "id": "routine",
    "label": "Create routine from this conversation",
    "label_cs": "Vytvořit rutinu z této konverzace",
    "icon": "calendar-clock",
    "capability": "routine.create",
    "form_schema": [
      {"name": "name", "type": "text", "required": true},
      {"name": "cron", "type": "cron", "required": true},
      {"name": "timezone", "type": "timezone", "default": "UTC"}
    ]
  },
  ...
]
```

i18n: command labels live in the platform catalog with both
`label` (en) and `label_cs` (cs). UI picks based on user locale.

Static catalog ships with: `routine`, `skill`, `credential`, `issue`,
`remember`. Each maps to a capability name. Adding a new slash
command in the future = one entry + one handler wire-up; no schema
changes.

### 6.7 Frontend — slash palette + Members grid

**HTML wireframe step first** (per project convention). Two
mockups land under `/tmp/wireframes/` as static HTML for sign-off
before any React touch:

- `slash-palette-actions.html` — chat composer with `/` opened,
  actions group visible alongside existing chat/view/tools groups
- `members-capabilities.html` — Workspace Settings > Members,
  per-member capability checkbox grid

After sign-off:

- `components/features/chat/composer/slash-palette.tsx` extends with
  `actions` group, populated from `/api/v1/slash-commands` (TanStack
  Query, 5-min stale time, refetch on focus)
- `components/features/chat/composer/slash-action-modal.tsx` (new) —
  generic form renderer from `form_schema`, submits to the matching
  capability-gated endpoint
- `app/(dashboard)/settings/members/page.tsx` extends member row
  editor with capability checkbox grid; admin-only (role >= ADMIN)
- New shared component `components/admin/capability-grid.tsx` —
  checkbox grid driven by the same capability constants used in Go
  (TypeScript codegen step from a single `capabilities.ts` source
  of truth; avoids drift)

Empty conversation behavior: slash action always opens with empty
form (per §6 Q3 decision — no context-based hiding). If conversation
exists, certain fields can pre-fill (e.g., `/routine` form pre-fills
`name` from the conversation title).

### 6.8 CLI — server-driven slash + admin commands

Two pieces:

**REPL surface** (`internal/cli/repl.go`):

- On REPL start, fetch `GET /api/v1/slash-commands` and merge with
  file-based commands from `~/.crewship/commands/*.md` (existing
  `slashcmd.go` infra). Server-driven commands have a sentinel
  prefix (`@`) in autocomplete so the user can tell them apart from
  their own templates.
- On `/<server-command>` input, the REPL prompts for form fields
  inline (textinput for `text`, validated parser for `cron`, etc.)
  then POSTs to the matching capability-gated endpoint.
- TTL 5 minutes on the slash list cache; manual refresh via
  `/refresh` meta-command.

**Admin command surface** (`cmd/crewship/cmd_admin.go`):

```
crewship member list                                           # list members + their capabilities
crewship member capabilities <user>                            # show capabilities for one user
crewship member grant <user> <capability> [<capability>...]    # add capability
crewship member revoke <user> <capability> [<capability>...]   # remove capability
crewship member preset <user> <chat|power|admin>               # apply preset bundle
```

Preset bundles:

- `chat` — `["chat"]`
- `power` — `["chat", "routine.create", "issue.create", "memory.write"]`
- `admin` — full set including credential management

All admin commands require caller role >= ADMIN, audited.

## 7. Verification

- **Migration test** under `internal/database/migrations_test.go`:
  exercise old-data fixtures, assert backfill rule per role
- **Capability gate test** for each of the 5 actions × 3 personas
  (capability granted / capability missing / autonomous agent) = 15
  test matrix in `internal/api/capabilities_test.go`
- **Identity propagation test**: round-trip from CLI/chat-bridge
  through sidecar to backend handler, assert audit log carries
  user_id when slash-initiated, `system` when agent-initiated
- **Slash registry test**: capability-filtering correctness for
  each (role, capability set) pair
- **CLI smoke**: `crewship repl` → `/routine` → submit → routine
  exists → audit log shows the user
- **E2E**: Playwright spec walks Pavel granting Ludmila
  `routine.create`, Ludmila opening `/routine` in chat, submitting,
  routine appearing in workspace
- **Single-operator regression**: existing OWNER user can still do
  everything they did pre-PR (backfill correctness, no breakage)

## 8. Decision log

- **Capability JSON on workspace_members, not a separate table.**
  Single join; <50 capabilities expected; JSON1/JSONB is well-handled
  in both SQLite + Postgres. Separate table earns its keep only at
  per-crew-override scope, which is non-goal.
- **Capabilities layered on roles, not replacing them.** Roles still
  govern broad surfaces (settings UI access, admin commands).
  Capabilities govern slash-initiated end-user actions. Two
  orthogonal concerns; collapsing them confuses both.
- **Sidecar does no capability check itself.** Authoritative gate
  stays in the backend handler. Sidecar is a transport; mixing
  enforcement into a transport invites drift.
- **Dual-path in handlers (user vs. agent).** Same handler entrypoint,
  branches on `X-Caller-User-Id` presence. One business-logic path,
  two enforcement paths. Mirrors the `internal_hire.go` dual-entry
  pattern that's already proven.
- **Default capability `["chat"]` for new MEMBERs.** Safe default;
  admins explicitly opt-in additional capabilities. Backfill of
  existing rows uses role-aware bundle so single-operator installs
  upgrade without losing functionality.
- **Slash command labels server-side, i18n included.** Single source
  of truth for both Czech and English UI. UI doesn't hardcode any
  labels for server-driven slash actions.
- **`/remember` routes through existing memory write path.** Slash
  command is a UX entry point, not a separate write surface; same
  HITL verifier from PR #3 applies. No bypass.
- **No revoke-on-in-flight UX.** Admin revoking capability while
  user has form open: submit fails at backend re-check, user sees
  toast error. Acceptable for MVP.
- **HTML wireframes before React.** Per existing project convention;
  the two new UI surfaces are non-trivial enough to warrant it.

## 9. Open questions

- **Cache invalidation for slash list when admin updates
  capabilities mid-session.** TTL 5min for MVP; consider WebSocket
  push if user feedback shows the lag is noticeable
  (`internal/cli/websocket.go` is already in place).
- **Per-crew capability override (deferred).** A user might be MEMBER
  workspace-wide but routine-author in one specific crew. Separate
  table would be needed; deferred to v2 if demand surfaces.
- **Capability for `crew.spawn` (ephemeral hire).** `/spawn` exists
  in sidecar today (`spawn.go`) and is comment-marked LEAD-only but
  not enforced. Should it become a capability too? Defer — autonomy
  policy already gates it server-side and `/spawn` is not a slash
  action (no end-user UX for it).
- **Token-scoped CLI tokens vs. capabilities.** CLI personal-access
  tokens already have a `scopes` set (`rbac.go:202-229`). Capabilities
  and scopes are different concepts (capabilities = what the user can
  do; scopes = what the token can do). For this PR, capability check
  runs first; if the call is via a CLI token, the token's scopes
  AND the user's capability must both permit. Confirm with a test.

## 10. Sources

Internal:

- `internal/api/rbac.go` — existing role tiers and `requireRoleOrForbid`
- `internal/api/helpers.go:372-421` — role rank + `canRole`
- `internal/api/internal_hire.go` — dual-entry pattern reference
- `internal/sidecar/server.go:309-456` — sidecar route whitelist
- `internal/sidecar/spawn.go` — minimal sidecar handler pattern
- `internal/cli/slashcmd.go` — file-based CLI slash commands (will
  merge with server-driven)
- `components/features/chat/composer/slash-palette.tsx` — existing
  palette (will extend with `actions` group)
- `internal/policy/` — autonomy_level gate for autonomous agent calls
- `internal/memory/` — `/remember` dispatch target

PRDs:

- MEMORY-ROADMAP-2026.md — context for `/remember` capability and the
  HITL verifier this slash action must respect
- PRD-AGENT-EVOLUTION-2026.md — agent autonomy framework that the
  dual-path enforcement layers on top of

## 11. Out-of-scope items kept for visibility

- **SCIM/IdP group → capability bundle mapping.** Preset bundles are
  the seed; SCIM mapping is a thin layer above and lands in a
  follow-up. Enterprise customers who need it today can script via
  the admin CLI commands.
- **Per-resource ACLs.** `routine.create` is workspace-scoped, not
  "create this specific routine". Per-resource is a SaaS-tier
  concern; deferred until SaaS roadmap demands it.
- **Slash command marketplace.** File-based `~/.crewship/commands/`
  remains the extension point for power users; no shared catalog.
- **Capability inheritance across workspaces.** Each membership row
  has its own set; no cross-workspace propagation.
