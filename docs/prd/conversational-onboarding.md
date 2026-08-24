# PRD — Conversational onboarding (the setup agent)

> **Status:** Draft, revision 3. Not an issue, not scheduled. Blocked on a
> prerequisite that is itself a live production bug (§3).
> **Author context:** Written from a direct read of the tree on 2026-08-22,
> then attacked by five parallel investigations whose brief was to kill it.
> Revision 2 corrected three claims they broke; revision 3 corrects four more,
> including one revision 2 introduced. Every `file:line` was verified that day —
> re-verify before implementing.
>
> **Lifecycle update (2026-08-22):** the temporary-only lifecycle in this
> document has been superseded by [`crewship-guide.md`](crewship-guide.md).
> The reserved setup crew still supplies the safe onboarding proposal flow,
> but its agent is retained afterwards as Crewship Guide. Security findings in
> §5 remain binding; in particular, persistence does not grant user-session
> authority to the agent.
> **Mockup:** https://claude.ai/code/artifact/a77f6a32-ac91-425e-a20a-2c9bce84776b
> **Read §12 first.** It lists every claim that was tested and failed, across
> both revisions, so the next reader knows which sentences were checked.

---

## ✅ THE SHAPE

1. **Onboarding ends in a conversation, not a template grid.** After workspace,
   language and credential, the right panel becomes a chat with a *setup agent*
   that asks what the user needs and proposes a crew.
2. **The agent proposes; the human applies.** Nothing is written until the user
   clicks Create on a concrete proposal. This is a security control, and it only
   works if the card is rendered from a **server-stored** proposal and Create
   submits that proposal's **id**, never agent-authored content (§5.6).
3. **The setup agent authenticates as an agent, never as the user.** This is a
   hard architectural constraint, not a preference. It is the single decision
   that determines whether any other control in §5 exists at all (§5.1).
4. **Templates stay** — as the skeleton the agent selects and adapts.
5. **It cannot be built today.** Onboarding-created crews get no environment
   at all (§3), so the setup agent would be a chat window that never answers.

---

## §1 — Why

The wizard asks a first-time user to pick among four crew templates before they
know what a crew is, and those templates encode *our* use cases
(`internal/database/builtin/crew-templates/`, 12 files), not theirs.

The framing that motivated it: *"instead of preparing our use cases for him, he
arrives and writes — I need to scrape listings from Seznam — and you build him a
crew that handles it."*

**Reliability argument, corrected.** Revision 1 claimed this cuts first-run from
"four containers to one". That is false: `provider.CrewConfig`
(`internal/provider/container.go:24-64`) has no agent-scoped field, and both the
Docker provider (`docker_container.go:495`, `containerName := p.CrewContainerName(team.ID, team.Slug)`)
and the chat path (`chatbridge/bridge.go:477`, `containerKey := info.CrewID`)
key on the crew. **A four-agent crew already runs in one container.**

What survives is narrower and still real: a template deploy today creates four
agent rows that must each resolve a system prompt and a session inside that one
container, and if the shared devcontainer build fails all four fail together. A
single-agent setup crew reduces the number of things that must be right on first
run; it does not reduce container count. Do not sell this on the four-to-one
number — a reviewer will check it, as one did.

---

## §2 — What it is not

- Not a replacement for templates; they remain the one-click path.
- Not a general-purpose assistant. It exists for one onboarding session.
- **Not a credential surface.** The API key stays in the form on the left. See
  §5.5 — this is the one place the existing credential defences do not reach.

---

## §3 — Blocking prerequisite: onboarding creates crews with no environment

Live bug, predates this PRD, nothing here ships until it is fixed.

**Symptom, demonstrated end to end through the CLI on 2026-08-22** — not
inferred from the code. Empty database → `crewship init` → `login` →
`workspace use` → `crew create` → `agent create` → `credential create` +
`assign` → `crewship run`:

```
stdbuf: failed to run command 'claude': No such file or directory
[error] agent exited with code 127
```

The server log shows every prior link working, which is what makes this precise
rather than a general "onboarding is broken":

```
team container ensured   container_id=d6806f2e99c6
sidecar started          credentials:1
sidecar ready            credentials:1
tmux setup failed, falling back to direct exec   (graceful, by design)
exec agent               cmd:["claude","--print",…]
agent exited with error  exit_code=127
```

The container starts, the sidecar is healthy, the credential is delivered. The
adapter binary does not exist in the image. Everything upstream of the agent
process is fine; the environment is empty.

Note also what the operator sees: `agent exited with code 127 — check the
journal for details`. The one fact that resolves it — *`claude` is not
installed* — is in the container's own stderr and never surfaces. §5 of
`chatbridge` classifies container-start failures but not adapter-exec failures.

**Mechanism.** Four `INSERT INTO crews` statements omit `devcontainer_config`
and `runtime_image` from their column lists entirely, so all four produce NULL:

| Site | Path |
|---|---|
| `internal/api/crew_templates.go:150-155` | onboarding, template |
| `internal/services/onboarding.go:97` | onboarding, "Start blank" |
| `internal/api/recipes.go:312` | recipe install |
| `internal/api/internal_status.go:160` | **agent-created crews** |

`EnsureCrewRuntime` then falls through `CachedImage > Image > provider default`
to `internal/config/config.go:225`. Only `crews_create.go` accepts the columns,
and only when a caller supplies them.

**The fourth row is the one that matters for this feature.**
`internal_status.go:160` is `CreateCrew` — the endpoint §5.2 names as the setup
agent's write path. The door this PRD proposes to write through is itself part
of the outage it is blocked on.

**Dated.** Regressed **2026-04-15**, commit `8780f3c4` (PR #154). Before it the
default was `ghcr.io/crewship-ai/agent-runtime:latest`, which had claude, node
and uid 1001 baked in, so a NULL config was harmless. That commit deleted the
image, switched the default to bare Debian, added the columns in migration v46
**with no backfill**, and did not touch `deployCrewTemplate`. Seed data got its
own config six weeks later (`224149eb`, 2026-05-24). Templates never did.

**Why it hid for four months.** `./dev.sh seed` produces working crews because
`cmd/crewship/seeddata/builtin/crews.yaml` carries a full config, and every
development path uses seed. Agent chat has **no automated coverage anywhere** —
every CI path that could start a real sidecar sets `CREWSHIP_SKIP_SIDECAR=1` or
is gated behind an unconfigured `secrets.SEED_ANTHROPIC_API_KEY`, so it skips
silently every night.

**Fix.** Apply a default at one chokepoint, not at any of the five creation call
sites. The repo solved this class once already: `internal/crewstart` exists
because thirteen callers each assembled their own `CrewConfig` and three forgot
`CachedImage` (#1717); the fix was one funnel plus
`internal/crewstart/chokepoint_test.go`.

Verified-good default — built and run on 2026-08-22, `claude --version` → 2.1.239
as uid 1001:

```json
{
  "image": "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
  "containerEnv": { "PATH": "/home/agent/.local/bin:/home/agent/.local/share/mise/shims:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" },
  "features": {
    "ghcr.io/devcontainers/features/common-utils:2": {
      "username": "agent", "uid": "1001", "gid": "1001",
      "installZsh": false, "upgradePackages": false
    },
    "ghcr.io/devcontainers-extra/features/claude-code:2": {}
  }
}
```

Narrower than the seed config, which installs five vendor CLIs; only Claude Code
is verified, and the Cursor/Droid installers are `curl | bash` from two more
domains behind `|| true`.

**Hazard.** Feature installs are hard failures —
`internal/devcontainer/dockerfile.go` emits `bash -e ./install.sh`. On a host
that cannot reach `ghcr.io` or `downloads.claude.ai`, a feature-installing
default turns today's silent no-op into "crew creation fails", which is worse.
Provisioning is already async (`maybeAutoProvision`, `internal/api/crews.go:137`)
with a lazy dispatch-time fallback, so: create the crew, mark it not-ready, say
why. Never block, never pretend.

---

## §4 — The conversation

### 4.1 Flow

1. The setup crew is created and provisioned, with visible progress. This is the
   moment the container either works or does not.
2. The agent opens in the language chosen in step 1 (§6, already wired).
3. One open question, at most two clarifying. It does not interview.
4. It emits a **proposal**: named crew, named agents with roles and models, and
   any egress domains needed.
5. The user clicks Create, Edit, or asks for something else.
6. On Create the objects are written and onboarding completes — subject to §5.4,
   which is not a detail.

### 4.2 Non-negotiables

- **Concrete, not prose.** Names, roles, models. A paragraph is not a proposal.
- **Network is on the card.** Egress must never be granted as a side effect of a
  sentence. Note this is *not* satisfiable by the template path: the crew INSERT
  in `deployCrewTemplate` has no `allowed_domains` column and hardcodes
  `network_mode`. Showing domains on the card requires the new write path (§6).
- **Nothing is written before Create**, and the card the human read is exactly
  what executes (§5.6).

### 4.3 Fallbacks

- Skipping to a template must stay available; time-to-value must not regress.
- If the setup container fails to provision, fall back to the template grid with
  a visible reason. **Stated locally because a reader of this section alone
  would get it wrong:** before Phase 0 lands, the template path carries the
  identical NULL-config bug (§3), so this is a fallback to an equally dead crew.
  After Phase 0 the chokepoint default repairs both paths at once, because it
  applies downstream of every INSERT. The fallback is only meaningful
  post-Phase-0, and even then only when the failure is specific to the setup crew.

---

## §5 — Security

### 5.1 The decision everything else depends on

Every control below is enforced on `/api/v1/internal/*`, where `requireInternal`
(`internal/api/internal.go:358`) and the autonomy gate
(`internal/api/internal_autonomy_gate.go`, `gateInternalAction`) live. **The
public surface never calls `policy.DecideAction` at all.**

Revision 1 called the user-session path "the natural implementation". If it is
implemented that way, the onboarding user is always OWNER — top of `roleRank` —
and passes every `canRole` check in the app. `POST /crews` with
`network_mode:"free"` (`crews_create.go:170-177`), agent creation, credential
creation, MCP install all succeed with **zero** of the gate, the policy engine,
or the inbox hold ever running.

The controls would not be weakened. They would be **absent**.

> **Constraint:** the setup agent authenticates with a crew-bound internal token,
> minted for the nominal onboarding crew before the conversation starts. Its
> tool surface must never be able to carry the user's session JWT or cookie.
> Pin this with a chokepoint-style test (`internal/crewstart/chokepoint_test.go`
> is the idiom).

**Correction to revision 1's citation.** §5.2 claimed raising autonomy needs
`roleManage` (ADMIN+). The route is registered with **`roleCreate`**
(`internal/api/router_crews.go:163`), which admits MANAGER as well as
OWNER/ADMIN. The conclusion — an agent cannot reach it, because it is
human-JWT-only either way — still holds; the evidence cited for it did not.
Whether MANAGER should be able to set `autonomy_level: "full"` on any reachable
crew is a separate question worth asking outside this feature.

### 5.2 What the internal surface already refuses

| Capability | Available to an agent? | Evidence |
|---|---|---|
| Create a crew | Yes, autonomy-gated | `internal_status.go:82` |
| Create an agent | Yes, autonomy-gated | `internal_status.go:275` |
| Assign a credential to an agent | **No route** — removed deliberately | `internal/sidecar/server.go:539` |
| Read credential values | **No** — `include_values` is loopback-only | `internal_credentials.go:89-134` |
| Change network policy | **No** — public route only | `router_crews.go:150` |
| Install MCP servers | **No** — boot payload only | `mcp_gateway.go:246` |
| Create/rotate credentials | **No** — needs a signature nothing mints | `internal_credentials_mutate.go:108-155` |

Credential plaintext reveal additionally requires a live browser session
(`AuthKindSession`) and explicitly fails for bearer and internal tokens
(`credentials_reveal.go:416-423`).

New crews default to `restricted` egress with an empty allowlist
(`internal/database/crew_defaults.go:13`), resolving to LLM-provider domains
only (`internal/egressallow/allowlist.go`).

### 5.3 The autonomy-inheritance invariant does not cover this

The #1768 fix guarantees a created crew never outranks its creator
(`internal/policy/types.go:89-110`, pinned by
`TestAutonomyInvariant_ChildCrewNeverOutranksCreator`). It fires on the internal
path and needs **an existing calling crew** to inherit from. The setup agent runs
before any crew exists. This must be built, not inherited.

**The "nominal onboarding crew" is not a design yet.** Revisions 1 and 2 disposed
of this in one paragraph. It carries at least five unanswered questions, and the
feature is not approvable until they are answered:

1. **Token binding.** `autonomySubjectCrew` (`internal_autonomy_gate.go:115-120`)
   takes the crew bound to the internal token, else the crew named in the
   request. A workspace-bound (`wsv1`) token that names its target crew is
   covered; one that names nothing gets the resolver's safe default and is
   *held*, not waved through. So the fail-safe is real — but the nominal crew's
   fixed level is only consulted if the token is **crew-bound to it**, or the
   request names it. That wiring does not exist and must be specified.
   *(An earlier review claimed the level is "never consulted" and the call
   silently proceeds at a hardcoded default. That overstates it: the documented
   fallback holds the call. Recorded so the overstatement is not inherited.)*
2. **Visibility.** There is no crew "kind" or hidden flag in the schema, and
   `CrewHandler.List` (`crews_query.go:13-55`) has no filter. The nominal crew
   would appear in the user's crew list and every count on day one.
3. **Who deletes it.** The only crew-delete path is session-authenticated and
   role-gated. The setup agent's internal token cannot call it. Teardown needs
   new server-side code, plus somewhere to remember the nominal crew's id — no
   such column exists on `users` or `workspaces`.
4. **Deletion vs the audit trail.** Migration v167 deliberately moved journal
   foreign keys off `ON DELETE CASCADE` so deleting a crew does not erase its
   history. Any teardown must not undo that — and §5.9 calls that history the
   point of the whole exercise.
5. **Slug collision.** `UNIQUE(workspace_id, slug)` applies. The nominal crew
   needs a slug that cannot collide with whatever the user names their real crew
   minutes later.

### 5.4 The contradiction revision 1 did not see

Control "run at a fixed autonomy level" is self-defeating as written:

- `strict` → `ActionCrewCreate` and `ActionAgentCreate` are **Rejected**.
  Onboarding creates nothing.
- `guided` / `trusted` → creation succeeds but lands `PENDING_REVIEW` / pinned
  `strict`, requiring an OWNER/ADMIN inbox approval
  (`internal_status.go:150-208`) before the crew is usable.

Either way §4.1.6's "on Create the objects are written and onboarding completes"
does not hold — unless a bespoke path skips the gate, which voids the control.

**Resolution required before implementation.** Add an explicit decision arm for
onboarding-kind crews rather than special-casing a bypass. The human click on
Create *is* the approval; the design must make the policy engine agree, not route
around it.

### 5.5 The one defence that does not apply

Credential reveal protects **stored** values, not one the user is typing into
the chat. An injected agent could exfiltrate it before it is persisted, and the
model's own provider domain is on the egress allowlist by construction.

Structural mitigation: the key never enters the chat. Additionally a client-side
input scrubber for provider-key shapes, documented as probabilistic, not a
boundary.

### 5.6 Proposal integrity — the card must not be able to lie

The question: if the card is rendered from agent-authored text, can it display
"3 agents" and create 30, or hide a domain?

Two existing patterns answer it, and they should both be copied.

**`internal/manifest` Plan/Apply.** `BuildPlan` (`plan.go:36-48`) produces items
each carrying a rendered description **and** an opaque `exec` closure captured at
plan time. `Apply` (`apply.go:159-165`) reports every line *before* running a
single `exec`. The load-bearing property: **the human-readable line and the
mutation come from the same struct, captured at the same moment.** There is no
second path that re-derives what to execute after approval.

**The inbox decide contract.** `POST /approvals/{id}/decide`
(`approvals_handler.go:116-221`) accepts only `{status, comment}` — no content
fields. A decision cannot be tricked into approving different data.

**Therefore:** the proposal is a server-stored object; the card renders from it;
Create submits its **id**. The agent never re-authors the payload at click time.

### 5.7 The time-box escape

`ActionRoutineScheduleCreate` is **not held** at `guided` or `trusted`
(`policy/types.go:410-437`): the schedule is created **enabled**, with only a
non-blocking notice. Only `strict` rejects it.

So at whatever level §5.4 settles on, the setup agent could create a live cron
that outlives the conversation and the container. Simplest fix: never register
`POST /internal/routines/schedules` for an onboarding-bound token. Also verify
whether torn-down crews cascade-disable `pipeline_schedules` — not established.

### 5.8 No object cap exists

`crews.max_ephemeral_agents` is explicitly not applied to persistent creation
(`internal/policy/types.go:106`: "no TTL, no template, no max_ephemeral_agents
quota"). Nothing counts prior calls. A card saying 3 does not stop 300.

Needs a real counter inside the gate, mirroring `agents_hire.go`'s
live-count-then-reject pattern, with a named constant and a boundary test.

### 5.9 No forensic trail — and the gap is narrower than revision 2 said

Revision 2 claimed no audit row is written from `crews_create.go` or
`agents_create.go`. **That is false.** `crews_create.go:336` calls
`auditFromRequest`, and `agents_create.go:274` calls `WriteAuditLog`. The
agent-initiated path audits too (`internal_status.go:210-215`).

The real gap is more specific, and the distinction changes the fix:
**onboarding's own inserts bypass those audited handlers entirely** —
`deployCrewTemplate` and `internal/services/onboarding.go` write their own SQL
and never call into them, and `onboarding.go:542` passes `noopEmitter{}`.

So the fix is *not* "add audit logging to those handlers" (they have it). It is
**route onboarding through the audited handlers**, or add equivalent calls to
onboarding's own inserts. §9's success criterion additionally needs a
proposal/turn correlation field, which no audit schema carries today.

### 5.10 Trust boundary

With `CREWSHIP_BOOTSTRAP_WINDOW` unset — the default — bootstrap stays open with
no expiry until a user exists (`internal/api/auth.go:48-52,206-211`). On an
internet-exposed instance whoever reaches `/bootstrap` first becomes OWNER and
lands on onboarding. This is pre-existing; what changes is that the race now wins
a conversation with a writing agent. Treat all user input as untrusted text.

### 5.11 Adjacent finding — not part of this PRD

`startSidecar` (`internal/orchestrator/exec_sidecar.go:1041-1054`) passes the
boot payload as `Cmd: []string{"sh", "-c", script}`. The script embeds a base64
payload containing every provider credential in plaintext, the crew-bound
internal token, and every crew member's `AuthToken`. `/proc/<pid>/cmdline` is
mode 0444, readable by the co-resident agent uid. The credential-**files** path
was fixed under #1646 by joining the merged preflight batch; `startSidecar` has
no such branch. This defeats the stated purpose of per-member tokens
(`sidecar/server.go:47-51`: "so a shared-container sibling can't impersonate the
boot agent").

`TestSidecarLaunchScriptKeepsCredentialsOffArgv` (added 2026-08-22) checks only
the child `crewship-sidecar` line and the stdin pipe. Both hold; neither covers
the parent `sh -c` argv. The test asserts a narrower property than its name.

Belongs in its own security issue. It overlaps the "get secrets OFF the agent"
item in `docs/prd/agent-identity-signing.md`'s locked-decisions preamble — item 7
of that list, which is not the same as that document's markdown §7.

---

## §6 — EXISTS / PARTIAL / MUST BUILD

| Item | Verdict | Evidence |
|---|---|---|
| Chat path browser → agent → back | **EXISTS** | `ws/client.go:84` → `chatbridge/bridge.go:367` → `orchestrator_run.go:68` |
| Structured card over the wire | **EXISTS** | `ws.ChatEvent.Metadata` (`hub.go:63-66`); `askforms` already round-trips typed envelopes through it (`askforms/envelope.go:50`) |
| Language into the system prompt | **EXISTS** | `onboarding.go:384` → `orchestrator_run.go:865-871` `[LANGUAGE]` block |
| Chat during onboarding | **PARTIAL** | Nothing gates it on `onboarding_completed`; the blocker is that no crew/agent/chat rows exist yet, and `setupFromTemplate` flips the flag *first* (`onboarding.go:405-431`). Needs sequence inversion. |
| Create = template + model swap | **EXISTS** | `deployCrewTemplate` + `deployOverrides` (`crew_templates.go:85-107`) |
| Create = N custom agents, atomically | **MUST BUILD** | `deployCrewTemplate` reads agents only from `agents_json`; public path is 1 crew-create + N agent-creates, non-transactional, no rollback |
| Egress domains on the card | **MUST BUILD** | template INSERT has no `allowed_domains` column |
| Nominal onboarding crew for the gate | **MUST BUILD** | §5.3 |
| Teardown on completion | **PARTIAL** | crew delete force-removes *sidecars* only; the runtime container is deliberately left to a 4-hour idle TTL (`crew_sidecar_teardown.go:41-43`, `crew_resource_policy.go:112`). Chat rows are orphaned, not cleaned. |
| Audit / journal on onboarding writes | **MUST BUILD** | §5.9 |
| Object cap | **MUST BUILD** | §5.8 |

---

## §7 — Phasing

**Phase 0 — unblock (ships alone, no feature work).** Default devcontainer at
the chokepoint; provisioning failure surfaces as not-ready with a reason; the
container+sidecar boot test of §8.3 — which needs **no API key**.

**Phase 1 — the seam.** Setup crew instead of a template deploy; chat in the
right panel; the agent proposes; Create runs `deployCrewTemplate` with at most a
model swap. Honest scope: this also requires the onboarding **sequence
inversion** (create objects before flipping `onboarding_completed`) and the
frontend chat wiring. It is smaller than Phase 3, not small.

> If the card is allowed to rename, add or drop an agent, edit a prompt, or show
> an egress domain, Phase 1 has silently absorbed Phase 3. Hold this line.

**Phase 2 — the gate.** Nominal onboarding crew; the §5.4 decision arm;
audit and journal; call allowlist; object cap; schedule creation excluded.

**Phase 3 — real customisation.** The atomic "one crew + N conversation-derived
agents + egress" endpoint. New work, no existing primitive.

---

## §8 — Test architecture

Test-first. Every item states whether the machinery exists.

### 8.1 Unit (Go, `setupTestDB`, per-PR)

| Test | Asserts | Must fail against |
|---|---|---|
| `TestOnboardingCrew_HasFixedAutonomyLevel_IndependentOfConversation` | The created crew's level is the pinned constant whatever the transcript argues for | Taking the level from a request field or the column default — the #1768 shape |
| `TestSetupAgent_NeverAuthenticatesAsUser` | No code path attaches a session JWT/cookie to the setup agent's calls | Routing Create through the public API |
| `TestSetupAgentAllowlist_EveryRouteIsOnTheList` | Structural walk of `router_internal.go`; a route reachable by an onboarding-bound token that is not on the maintained list fails the build | Adding a route without listing it. Idiom: `internal/crewstart/chokepoint_test.go:36-77` |
| `TestSetupAgentAllowlist_ExcludesRoutineSchedules` | `POST /internal/routines/schedules` is unreachable | §5.7's escape |
| `TestSetupAgentSession_RefusesProposalBeyondObjectCap` | Named constant; `N-1` ok, `N` ok, `N+1` a named rejection, not a silent truncation | A cap that drops the tail quietly |
| `TestSetupAgentWrites_EmitJournalAndAudit` | One row per created object, carrying the proposing turn | `noopEmitter{}` |
| `TestSetupAgentCrew_EgressIsLLMProvidersOnly` | Explicit allowlist, not empty-and-inheriting | Leaving it to the default |

### 8.2 Proposal integrity — the highest-value test

`TestProposal_CardRendersFromTheSameStructApplyExecutes`.

Build the proposal in the `manifest.PlanItem` idiom: each row carries its
rendered description **and** an opaque `apply` closure captured at propose time.
Render the card. Call Create. Assert every written field equals the rendered
card exactly — per field, not "close enough".

**The mutation it must fail against:** make Create re-derive any field from live
conversation state. Mutate that state between propose and click; if the executed
object differs from the card in one field, the test fails.

### 8.3 Container boot — no API key, per-PR

`TestOnboardingCrewImage_BootsARealSidecarAndAnswersHealth`.

Follow `internal/provider/docker/runtime_conformance_test.go`'s conventions but
**not** its fake-ELF shortcut (`:316-322`) — that shortcut is correct for a
runtime test and exactly wrong here, because the sidecar booting *is* the thing
under test. Build the real sidecar, go through the `internal/crewstart`
chokepoint, exec the real `sidecarLaunchScript`, hit `/health`, and assert
`claude --version` succeeds inside the container.

`/health` reads in-memory state only (`internal/sidecar/proxy.go:541`) — no LLM,
no credential. **This is the test that would have caught the four-month
regression, and it is the cheapest one in this document.** ~30–90 s.

### 8.4 CLI acceptance — the contract

Per CLAUDE.md the acceptance test drives the binary. Two shapes, matching what
exists: `scripts/e2e-agent-run-test.sh` (exit 77 = skip, 1 = fail with output)
and `scripts/test-harness/lib.sh` (`cs`, `assert_*`, `finish`, `PROVIDER_STATE`).

The sequence below was **executed on 2026-08-22**; four corrections came from
running it, not from reading:

```bash
# 1. Build BOTH binaries. The server refuses to boot without the sidecar
#    beside it — building only ./cmd/crewship fails at startup.
go build -o "$RUN/crewship" ./cmd/crewship
go build -o "$RUN/crewship-sidecar" ./cmd/crewship-sidecar

# 2. Assert the precondition. A stale server on the same port answers happily
#    and returns "410 Already initialized", which reads as a product bug.
curl -sf "$SERVER/api/v1/system/setup-status" | grep -q '"needs_bootstrap":true'

# 3. Empty DB → owner + workspace + token. No pairing, no browser, no seed.
crewship init --email … --name … --password-stdin

# 4. Parse the WHOLE token. It is `crewship_cli_<hex>`; taking only the hex
#    tail yields a plausible string that fails with "session_invalid".
crewship login --token "$TOK"

# 5. REQUIRED. init+login leave no workspace selected and every later command
#    exits 2 with "no workspace set".
crewship workspace use "$WS"

crewship crew create --name … --slug …
# 6. --slug is required; omitting it fails "slug must be 2-50 characters"
#    rather than naming the missing flag.
crewship agent create --name … --slug … --crew … \
  --cli-adapter CLAUDE_CODE --llm-provider ANTHROPIC --llm-model claude-sonnet-5
crewship credential create --name … --type AI_CLI_TOKEN --provider ANTHROPIC \
  --env-var-name ANTHROPIC_API_KEY --value-stdin
crewship credential assign … --env-var-name ANTHROPIC_API_KEY   # not optional

# 7. The contract command. First run pays for image build + container start.
crewship run <agent> "Hello, introduce yourself in one sentence." \
  --no-stream --timeout 600
```

`crew provision` / `crew start` are not required — the run path calls
`EnsureProvisioned` lazily.

**Known CLI gap that blocks a full assertion.** `crewship agent get --format json`
omits `llm_provider` and `llm_model`: `agentDetailResponse`
(`cmd/crewship/cmd_agent.go:276-320`) has no such fields, and `f.JSON(v)`
marshals the typed struct, so `--format json` is equally blind. **An acceptance
test cannot currently assert through the CLI that an agent got the model the user
chose.** Fixing this is in scope for whoever implements §8.4.

### 8.5 Frontend

Vitest for the proposal card: every agent name, role, model and domain rendered —
no summarisation; and the create mutation is **not** called on mount, re-render
or prop change, only on the click handler.

Playwright: extend the existing per-PR `onboarding-journey` job rather than a
nightly, for the reason `e2e-devcontainer.yml:20-27` already gives — "a nightly
cannot protect against this class of regression, because by the time a nightly is
red the regression is already on main".

**The comment trap, fourth occurrence.** Any `.not.toMatch` against real source
must run through a comment-stripper, because the comment explaining a correction
quotes the wording being corrected. `setup-chrome.test.ts:202-216` documents this
as "the third time that has bitten in this file". Worse,
`dead-agent-routes.test.ts:121-198` shows a **regex** stripper opening on a
`group/*` mentioned in prose and swallowing sixteen lines of real code — the file
passed while shipping a 404. Two independent implementations now exist, one
broken. **Promote the character-scanner version to a shared test util; do not
write a fifth copy.**

### 8.6 Anti-regression for the class of bug that caused this

- `TestCrewCreate_NeverPersistsNullDevcontainerConfig` — table-driven over every
  creation path; NULL after insert fails. Idiom: `chokepoint_test.go`.
- **A check that cannot run must not report a verdict.** Extend
  `scripts/test-harness-integrity.sh` to reject a suite with an assertion path
  and no corresponding skip branch when its precondition is absent.
- **Base-image contract gate.** Static scan of every `sh -c` exec site across
  `internal/orchestrator`, `internal/devcontainer`, `internal/provider/docker`;
  extract referenced binaries; fail the build against a maintained manifest of
  what the default image ships. This is the generalisation of the `wget`/`curl`
  bug, which was fixed once as a special case.

### 8.7 Cadence

| Tier | Runtime | When |
|---|---|---|
| Go unit, Vitest | seconds | per-PR |
| Container boot (§8.3, no key) | 30–90 s | **per-PR** — gating this nightly repeats the original mistake |
| Playwright onboarding | within the existing 25-min job | per-PR |
| CLI acceptance, no-credential half | 10–20 s | per-PR |
| Live-conversation half (needs a key) | minutes | nightly |

**A skipped job must be loud.** `nightly-harness.yml:729` already emits a
`::warning::` and names the suites that did not run. Add the new suite to that
list by name. Silence is what this whole document exists because of.

---

## §9 — Success criteria

- A fresh install with a working network and a valid key reaches a first agent
  reply **without reading any documentation**.
- Time-to-first-reply for a template user does not regress.
- Every created object appears in the audit log with the proposing turn.
- A prompt-injected setup agent cannot make anything live without a human click.
- The CLI acceptance test passes from an empty database, with no seed.

## §10 — Open questions

1. §5.4's contradiction: which decision arm does onboarding live in?
2. Should the bootstrap race (§5.10) be closed before this ships, now that it
   gates a writing agent rather than a form?
3. Is teardown worth building, or is the 4-hour TTL acceptable for a setup crew?
4. Offline hosts: is a bare, honestly-labelled crew better than a failed one?

## §11 — What is verified, and how

**By execution (2026-08-22):** the base image's contents and its lack of uid 1001;
`claude-code:2` building and running as uid 1001; `debian:bookworm-slim` lacking
curl/wget/ca-certificates; the sidecar passing a binary probe where the shell
probe failed; the entire CLI sequence in §8.4 including all four corrections;
**and §3's failure reproduced to its exact exit code (127, `claude` not found)
with the container, sidecar and credential all confirmed healthy first.**

**By reading:** every `file:line` in §5 and §6, each independently re-derived by
at least two investigations where it contradicts revision 1.

**Not verified:** offline/proxied hosts; whether `pipeline_schedules` cascade on
crew teardown; whether a soft-deleted agent stays resolvable for an orphaned chat;
whether the §3 chokepoint fix repairs an onboarding-created crew end to end (the
default image is verified, its application at the chokepoint is not — this is the
highest-value outstanding verification).

**One review's frontend findings were dismissed for a wrong reason.** An
investigation reported that frontend source "is not in this repository — `web/`
contains only `embed.go` and the compiled `web/out`", and marked every wizard-UI
claim unverifiable. That is false: the Next.js source is at the repo root
(`app/(onboarding)/onboarding/page.tsx` and siblings). Those claims are
verifiable and were simply not checked.

**Corrected from revision 2's own "not verified" list:** it said nobody knew
whether a harness suite drives a chat end to end. `scripts/test-harness/test-run-stream.sh`
does — gated behind the same unconfigured `SEED_ANTHROPIC_API_KEY`, which is why
it never runs. The gap is not "no such test"; it is "the test exists and is
silently skipped", which is worse and is what §8.7 addresses.

## §12 — What revision 1 got wrong

Recorded because the next reader deserves to know which claims were tested.

1. **"Four containers to one."** False. Crews are single-container regardless of
   agent count. The reliability argument survives in a weaker form (§1).
2. **"The blast radius does not change."** False as written. It holds only if the
   setup agent uses the internal path; on the public path every control is absent,
   not weakened (§5.1).
3. **"Run at a fixed autonomy level and the rest follows."** Self-contradictory:
   `strict` creates nothing, `guided`/`trusted` require an inbox approval that
   §4.1.6 denies (§5.4).
4. **Miscitation:** autonomy change was said to need ADMIN; the running gate
   accepts MANAGER (§5.1).
5. **Missed:** the routine-schedule escape (§5.7), the absent object cap (§5.8),
   and that the template path cannot write egress at all (§4.2).
6. **"Phase 1 needs no new write powers."** True only if the card is limited to a
   template plus a model swap — which §1 and §4.1 promised to exceed (§7).

### Corrected again in revision 3

7. **"No audit row is written from `crews_create.go` or `agents_create.go`."**
   False, and revision 2 introduced it. Both audit
   (`crews_create.go:336`, `agents_create.go:274`). The real gap is that
   onboarding's inserts bypass those handlers — which changes the fix (§5.9).
8. **§3 named two broken INSERT sites.** There are four, and the one it missed
   that matters is `internal_status.go:160` — the setup agent's own write path.
9. **The autonomy route cites the wrong role tier, twice.** It is `roleCreate`
   (`router_crews.go:163`). Revision 1 said `roleManage`; revision 2 said
   `requireRole("update")`. The conclusion held both times; the evidence did not.
10. **"The nominal onboarding crew is just missing plumbing."** It is five
    unanswered design questions (§5.3), not a paragraph.
11. **§4.3's fallback** read as if it worked today. Before Phase 0 it falls back
    to an equally dead crew.
12. **§11 listed as unverified** something an earlier investigation had already
    established (§11).

### A note on the reviews themselves

One adversarial finding was itself overstated — that the autonomy gate silently
proceeds at a default when no crew is bound. The code holds the call instead
(§5.3, item 1). Recorded because a PRD that absorbs every criticism uncritically
is no better calibrated than one that absorbs none.
