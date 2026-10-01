# Notification channels à la Uptime Kuma

Status: **Phase 1 (category audit) complete. D1 + D2 decided — phases 2–4 ready
to build.** Date: 2026-07-27. Extends #1412 (categories + prefs matrix) and #850
(run-terminal broadcast).

One live bug found by the audit (Finding 3) — an auto-disabled routine notifies
nobody. Its fix rides in the phase-1 migration, not as a separate patch, so the
`inbox_items.kind` CHECK is rewritten once.

---

## Phase 1 — Category audit (the prerequisite)

The brief's premise was "verify the existing categories cover the list below,
add what's missing." The audit found the gap is structural, not a matter of
adding constants.

### How an event actually reaches a channel today

```
                      ┌──────────────────────────────────────┐
  ~90 event types  ─► │  journal  (internal/journal)         │  audit log,
  (everything emits)  │  budget.exceeded, mission.status_…,  │  tamper-evident,
                      │  provisioning.failed, agent.error, … │  NO consumer
                      └──────────────────────────────────────┘  for notify
                                    ✗ no bridge

  6 inbox kinds  ────►┌──────────────────────────────────────┐
  waitpoint           │  inbox  (internal/inbox)             │
  escalation          │  Insert / UpsertMessage              │
  failed_run          └───────────────┬──────────────────────┘
  message                             │ ExternalNotifier seam
  memory_consolidation                ▼
  schedule_missed      ┌─────────────────────────────────────┐
                       │ notifyroute.Router                  │
                       │ categoryByKind → 5 categories       │
                       │ prefs × channel × priority × rate   │
                       └───────────────┬─────────────────────┘
                                       ▼
                              email / webhook / slack / discord / telegram
```

`internal/notify/categories.go:87` (`categoryByKind`) is the **only** producer of
a category anywhere in the codebase. Verified: the `Category*` constants have
zero references outside `internal/notify`. It maps 5 kinds:

| inbox kind | category |
|---|---|
| `waitpoint` | `approvals` |
| `escalation` | `escalations` |
| `failed_run` | `runs.failed` |
| `message` | `chat.replies` |
| `memory_consolidation` | `memory` |

### Finding 1 — 4 of the 9 categories can never fire

`runs.completed`, `security`, `budget`, `system` have no kind mapping and no
other producer. They render as rows in the preference matrix
(`notification-prefs-section.tsx`) and as checkboxes in the channel category
allowlist, and a user can switch them to `immediate`, but nothing will ever be
delivered against them. **44 % of the matrix is decorative.**

`runs.completed` is a partial exception: a channel still receives run
completions through the legacy #850 broadcast (`Dispatcher.Dispatch`, driven by
`Channel.Events`), but that path is workspace-wide and ignores the per-user
preference matrix entirely. The matrix row itself is dead.

### Finding 2 — `schedule_missed` is written but never routed

`inbox.KindScheduleMissed` (v162, #1422) writes an inbox card when a schedule
drops backlog occurrences per its `catchup_policy`. It is absent from
`categoryByKind`, so "your routine did not fire as scheduled" never leaves the
product. This is one of the five routine events the brief explicitly requires.

### Finding 3 — the circuit-breaker alert is broken at the DB level *(live bug)*

`internal/pipeline/schedules.go:1082` inserts an inbox item with
`Kind: "schedule_circuit_breaker_tripped"` when a schedule is auto-disabled
after N consecutive failures. That string is **not** in the `inbox_items.kind`
CHECK constraint, which v162 last widened to:

```
CHECK (kind IN ('waitpoint','escalation','failed_run','message',
                'memory_consolidation','schedule_missed'))
```

Verified against the real migrated schema (`Migrate()` to head, then insert):

```
kind=failed_run                        err=FOREIGN KEY constraint failed (787)   ← expected, no ws row
kind=schedule_missed                   err=FOREIGN KEY constraint failed (787)   ← expected, no ws row
kind=schedule_circuit_breaker_tripped  err=CHECK constraint failed: kind IN (…) (275)
```

In production the insert fails, `inbox.Insert`'s error is swallowed into a
`logger.Warn`, and **nobody is ever told their routine was auto-disabled** —
the single most important thing to know about a routine that has stopped
running. The cost-bleed protection works; the notification about it does not.

`internal/pipeline/schedules_circuit_breaker_test.go:115` asserts this alert
lands and passes green, because `openPinningTestDB` hand-rolls an `inbox_items`
table with no CHECK constraint. A false green of exactly the kind
`feedback_artifact_exists_is_not_proof_it_ran` warns about — the test proves the
code called Insert, not that Insert succeeded against the real schema.

### Finding 4 — Issues have zero notification coverage

The brief asks for issue created / state changed / assigned / commented /
blocked. `IssueHandler` (11 files, `internal/api/issue_handler*.go`) emits
WebSocket events via `broadcastIssueEvent` and journal entries
(`mission.status_change`, `mission.comment`), and writes **no inbox items at
all**. Nothing about an issue can reach a channel today. This is the single
largest coverage hole.

### Coverage against the brief, event by event

| Area | Event | Status |
|---|---|---|
| **Routines** | completed | legacy broadcast only — matrix row `runs.completed` dead |
| | failed | ✅ `failed_run` → `runs.failed` |
| | skipped | ❌ nothing emits |
| | didn't fire per schedule | ⚠️ inbox card written, **not routed** (Finding 2) |
| | auto-disabled (circuit breaker) | 🔴 **broken** (Finding 3) |
| **Issues** | created | ❌ |
| | state change | ❌ journal only |
| | assigned | ❌ |
| | comment | ❌ journal only |
| | blocked | ❌ |
| **Agents** | escalation | ✅ `escalation` → `escalations` |
| | approval request | ✅ `waitpoint` → `approvals` |
| | container error | ❌ journal `provisioning.failed` / `agent.error` only |
| | budget exceeded | ❌ journal `budget.exceeded` only — category `budget` dead |
| **System** | instance health | ❌ |
| | migration | ❌ journal `system.migration` only |
| | security events | ❌ journal `guardrail.*`, `network.egress` only — category `security` dead |
| | keeper verdicts | ⚠️ collapse into `escalations`; `keeper.decision` not routed |
| **Chat** | agent reply while away | ✅ `message` → `chat.replies`, with presence gate |

**5 of 18 required events are covered.** Four of the covered five are the ones
#1412 shipped with.

### Why this happened, and what it implies

`inbox` is a **human-attention queue**: every item is a card someone is meant to
read and often act on (`blocking`, `state: unread|read|resolved`). That is the
right model for approvals and escalations, and the wrong model for "budget
crossed 80 %" or "an issue moved to In Review" — nobody wants 40 inbox cards a
day, but they may well want those in Slack.

Hanging *all* external notification off the inbox is what forced the taxonomy
into 9 categories where only 5 have a producer. The journal already records
everything the brief asks for, with the right granularity, and has no consumer.

**The fix is a second source, not more inbox kinds.** A journal→category bridge
in `notifyroute` for observational events, with `inbox` remaining the source for
actionable ones. That keeps the existing prefs matrix, channel allowlist, mute
row, priority floor, rate gate, and delivery log unchanged — they all key off
`category`, not off where the event came from.

This is the decision that gates phases 2–4. See **Open decisions** below.

---

## Phase 2 — Provider-shaped forms + draft test

Not started. **Source material: Uptime Kuma** (decision, 2026-07-27) — see
"Adopting Uptime Kuma" below for what is and isn't liftable. Scope confirmed
against the code:

- **The form.** `notification-channels-section.tsx:197-222` is one "Service URL"
  input whose help text tells the user to type `discord://token@channel`, with
  no indication of where a token comes from. Replace with per-provider field
  sets — Discord: Webhook URL (+ optional bot display name); Telegram: Bot Token
  + Chat ID; Slack: Incoming Webhook URL — each with a one-line "where do I find
  this" hint and a link. Compose the `shoutrrr://` URL **server-side** in
  `ChannelStore.Create`; the user never sees or types it.
  - `ChannelInput.ShoutrrrURL` stays as the internal representation. Add a
    `ProviderFields map[string]string` input and a per-provider composer +
    validator. Keep accepting a raw URL on the CLI for scripting.
  - One-time reveal (`createChannelResponse.Secret`) currently shows the
    composed service URL. Once the user supplies the parts, revealing it back is
    pointless — drop the reveal for shoutrrr channels, keep it for webhook HMAC.
- **Test before save.** `POST /notification-channels/{id}/test` requires a
  persisted row (`GetForDispatch`). Add `POST /notification-channels/test` that
  takes an unsaved draft, composes, dispatches once, and persists nothing.
  Rate-limit it and require the same role as create, or it becomes an
  unauthenticated outbound-request primitive.
- **Delete the word "shoutrrr" from user-visible text.** 26 occurrences in
  TS/TSX, 13 in `docs/guides/notifications.mdx`, 17 in `cmd_notifychannel.go`.
  Note `--type shoutrrr` is a CLI flag *value* and part of the API contract —
  rename to `--type chat` with `shoutrrr` kept as a hidden alias, don't break it.
  The DB `type` column can keep its value; nobody sees it.

## Phase 3 — Agent-initiated sends

Not started. The seam already exists: sidecar MCP tools (`save_routine`,
`run_routine`, `discover_capabilities` in `internal/sidecar/routine_mcp.go`,
plus `memory_mcp.go`) are how an agent reaches Crewship from inside its
container, authorized by the agent-derived internal token
(`internaltoken.ForAgent`). Add a `notify_send` tool the same way.

Open sub-questions, all of which are authorization design, not plumbing:

- **Pairing.** Which channels may a given agent post to? Proposal: an explicit
  per-channel agent allowlist, default empty — an agent posts nowhere until a
  human pairs it. Never "any workspace channel."
- **Category.** Agent sends should carry a category so they obey the same
  matrix. Proposal: a new `agent` category rather than letting an agent claim
  `security` or `approvals` and bypass a user's mute.
- **Rate limit.** `notifyroute.RateLimiter` (token bucket, burst 5, refill
  1/30s, keyed by recipient×channel×category) already exists and should be
  reused with an agent-scoped key. The brief's "one chatty agent must not flood
  Slack" is exactly this, and `BypassesRateGate` must **not** apply.
- **Scrubbing.** `Dispatcher.scrubPreview` runs on the legacy path. Agent-authored
  text is untrusted (`internal/untrusted`) and must go through the scrubber
  before it leaves the instance.

## Phase 4 — Move to Integrations

Decided (D2): channels **and** the preference matrix move to Integrations;
Settings → Notifications becomes a redirect. Work:

- Notification channels become a provider family on the Integrations page,
  alongside Composio and MCP — one card per connected channel, plus an
  "Add integration" catalogue driven by the provider schema.
- **Admin overview**: an admin-only view listing every connection in the
  workspace — type, provider, scope, owner, enabled, last delivery — including
  other members' personal channels. Metadata only; secrets stay unreadable
  (`ChannelStore.List`'s viewer filter continues to apply to non-admins, so this
  needs a separate admin-scoped read, not a relaxation of the existing one).
- The per-user category × channel matrix moves in as a tab. It stays per-user;
  only its location changes.
- `settings-layout.tsx:51` loses the `notifications` section; the route
  redirects so existing links and docs don't 404.

---

## Decisions taken (2026-07-27)

### D1. Taxonomy — full expansion, ~14 categories

Chosen over the two smaller options. Rationale: the five groups in the brief are
what a user actually reasons about when deciding "ping me in Slack for this, not
that," and collapsing routines into one row means you cannot subscribe to
failures without also getting completions. The matrix cost is real and is
mitigated in the UI (group headers, collapse-by-group, a per-group bulk toggle)
rather than by shrinking the vocabulary.

```
routines.completed   issues.created      agents.escalation   system.health
routines.failed      issues.state        agents.approval     system.migration
routines.skipped     issues.comment      agents.error        security
routines.missed      issues.assigned     agents.budget       chat.replies
                                                             memory
```

Migration consequences — do these **once**, in a single migration:

- `inbox_items.kind` CHECK widened to the final set, including the
  `schedule_circuit_breaker_tripped` value that is broken today (Finding 3).
- `user_notification_prefs.category` and `notification_channels.categories_json`
  carry the new vocabulary. Existing rows must be **migrated, not dropped**:
  `runs.failed` → `routines.failed`, `runs.completed` → `routines.completed`,
  `approvals` → `agents.approval`, `escalations` → `agents.escalation`,
  `budget` → `agents.budget`, `system` → `system.migration` + `system.health`.
  A user who muted a category must stay muted afterwards.
- `notification_deliveries.category` is a historical log — rewriting it would
  falsify past deliveries. Leave old rows on the old vocabulary and have the
  reader map for display.

### D2. Placement — Integrations *(reversed from the earlier recommendation)*

Earlier recommendation was Settings-with-a-deep-link. **Overruled**, on grounds
that outweigh the domain-purity argument:

- Crewship is a platform with dozens of people in one workspace. Settings reads
  as "my personal preferences"; a fleet of connections that an org depends on
  does not belong behind a personal-settings door.
- An **admin needs one page showing every connection in the workspace** —
  Composio, MCP, notification channels — to have a total picture of what this
  instance is wired into and who wired it. That page is Integrations. Splitting
  notification channels out of it makes that overview structurally impossible.
- Agent discoverability: an agent asking "what can I reach?" should find one
  registry, not two.

Consequence: the **connections** move to Integrations, admin-visible across the
whole workspace including other members' personal channels (metadata only —
never a decrypted secret; `ChannelStore.List`'s viewer filter stays the rule for
non-admins). The **per-user category × channel matrix** follows them there as a
per-user tab, so there is exactly one place. Settings → Notifications becomes a
redirect, not a second surface.

---

## Adopting Uptime Kuma

Decision (2026-07-27): take Uptime Kuma as the source of the provider catalog
and form design rather than inventing our own.

**License — clear.** Uptime Kuma is MIT, "Copyright (c) 2021 Louis Lam"
(verified against the upstream `LICENSE`). MIT is compatible with Crewship's
Apache-2.0 core; the copyright line and MIT text must travel with anything we
copy. Caveat: `scripts/gen-notices.sh` derives `THIRD-PARTY-NOTICES.md` from
`go.mod` via `go-licenses`, so **copied source is invisible to it** — this needs
a hand-written entry, the same treatment as the existing `modernc.org/mathutil`
exception.

**What we take.** Their 169 files under `src/components/notifications/` are form
definitions: per provider, the field list, labels, required flags, placeholders,
help text, and "where do I find this" links. That catalog *is* the good UX the
brief is pointing at, and it is data — portable into a declarative
provider-schema table that drives both the React form and server-side
validation. `Discord.vue`, for example, yields: Webhook URL (required), Bot
Display Name, message format, thread/forum post ID with a link to Discord's
"Where can I find my User/Server/Message ID" page.

**What we cannot take.** Their 169 *senders* under
`server/notification-providers/` are Node.js. Crewship delivers from a single Go
binary. These do not port; they get thrown away and replaced by shoutrrr, which
Crewship already vendors.

**The free win.** `shoutrrr v0.16.2` is already in `go.mod` and already
supports **27 services**:

| group | services |
|---|---|
| chat | discord, googlechat, lark, matrix, mattermost, rocketchat, signal, slack, teams, telegram, wecom, zulip |
| push | bark, gotify, ifttt, join, mqtt, ntfy, pushbullet, pushover |
| incident | opsgenie, pagerduty |
| email / sms | smtp, twilio |
| specialized | generic, logger, notifiarr |

`notify.SupportedProviders()` exposes **3 of those 27** — an artificial
allowlist, not a capability limit. Opening it up costs a provider-schema entry
per service and no new delivery code. (`ntfy` is worth prioritising: the
infra stack already runs an ntfy server.)

So the plan is Uptime Kuma's *forms* over shoutrrr's *transports*, sized to the
27 shoutrrr can actually deliver to — not all 169.

---

## Deferred, deliberately

### Inbound listening (bidirectional channels)

Not in this scope: the Hermes / OpenClaw behaviour where writing to a Telegram
chat is heard by an agent that listens on that channel. The value is real —
a human drops into a channel the company already uses and the agent is simply
there — and the design should not preclude it.

What that costs us **now**, in the phase-2 schema, is small and worth paying:

- The provider schema should carry a `directions` field (`out`, `in`, `both`)
  even though every entry ships `out`. Retrofitting a direction onto a
  catalog is far worse than declaring one that is currently constant.
- A channel needs a stable identity independent of its outbound URL, because an
  inbound binding attaches to the channel, not to a webhook. `notification_channels.id`
  already satisfies this — just don't let the redesign key anything off the URL.
- Outbound delivery must not assume fire-and-forget everywhere. `Dispatcher`
  is already interface-shaped per channel type; keep it that way.

Explicitly *not* built now: long-polling/socket listeners, inbound message
routing to an agent session, per-channel identity mapping (who is this Telegram
user in Crewship terms), or the trust model for treating an inbound chat message
as agent input — the last is the hard part and belongs with
`internal/untrusted`.

### Instance-facing inbound webhook

Worth recording that most of the asked-for thing **already exists**:

- `POST /api/v1/webhooks/{crewId}/{agentId}/trigger` — triggers an agent run
  from an external call. HMAC-signed per agent, Stripe/Svix `ts.body` scheme
  with a 5-minute freshness window (`internal/webhook/handler.go`), secret
  rotatable via `POST /api/v1/agents/{agentId}/webhook-secret/rotate`, and the
  URL is already surfaced in the UI (chat → Triggers tab).
- `POST /api/v1/webhooks/{token}` — the routine/pipeline trigger equivalent,
  with `crewship routine webhooks url <id>` on the CLI.

Genuinely missing, and *not* part of this epic:

- **No configured public base URL.** The UI renders webhook URLs from
  `window.location.origin`, so no-DNS already works — but there is no
  `public_base_url` setting for the instance to advertise a stable external URL
  when DNS *does* exist. That is a small, contained config change.
- **Instance-to-instance trust** (a Crewship at home and a Crewship in the US
  verifying each other) is a different problem from receiving a Discord webhook,
  and should get its own brief rather than riding along here.

---

## Constraints that must not break

Verified present, all with existing tests:

- Channel scope: `workspace` (ADMIN/OWNER) vs `user` (self-service, owner-only
  read — `ChannelStore.List` filters other members' personal channels).
- The category × channel matrix including the `*` mute row
  (`CategoryMuteAll`, `ValidPrefCategory`).
- Webhook signing — `X-Crewship-Signature: sha256=<hmac>`.
- The routes are `roleInline`: authorization is role-**or**-ownership, decided
  in `authorizeChannelWrite`. Any UI gate must be `role || isOwner`, never role
  alone, or personal channels become admin-only.
- Provider enable/disable is instance-wide (`notification-providers`) and
  fail-closed at create.

## Project rules that apply

Tests first · every new endpoint gets a CLI command in the same PR · docs to
`docs/guides/*.mdx` in the same PR · semantic color tokens only · HTML wireframe
before the UI. One migration for the final `inbox_items.kind` set, not one per
phase.
