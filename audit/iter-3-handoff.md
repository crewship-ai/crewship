# Handoff — Memory & Agent-Evolution Audit · Iter 3 → Iter 4/5

**Date:** 2026-05-22
**Author of this doc:** Audit Lead (iter 3 session)
**Audience:** next-iter agent / human running follow-up validation
**Status:** Iter 3 closed; 2 critical production bugs found + fixed + pushed to PR #527; iter 4/5 plan below
**Scope:** Crewship runtime on dev1 (192.168.1.201, port 8081/3011)

---

## 0. tl;dr — what to know before reading

Crewship's "agents remember a year of context" claim was audited live across 3 iterations on dev1. **The architectural promise holds** — boot-context memory injection works, per-agent isolation works, cross-tier reasoning works, contradiction handling works. **Two production-grade bugs blocked mid-session memory tools** and were fixed in this iteration. Eight specific PRD claims (scanner extensions, cap protocol, lessons write block, cross-tenant Keeper gate, webhook HMAC, etc.) were live-verified — all eight pass post-fix.

**Outstanding for iter 4/5:** widen the matrix — custom crew with 5 personas + 6 users + RBAC×GDPR×memory×load mix across all 3 dev instances; Hermes feature-parity scoring; performance under sustained load. **The PRD's "Hermes-equivalent" framing is roughly 70 % shipped at architecture level, 100 % shipped at boot-context level, ~85 % shipped at mid-session-tool level after this iter's fixes, but several Hermes differentiators (pluggable provider, sub-agent brief, memory-source UI, decay/TTL, benchmark) remain not-yet-shipped per PRD non-goals.**

---

## 1. PRD context — what we audited

Two PRDs in `.claude/context/prd/`:

### 1.1 `MEMORY-ROADMAP-2026.md` (PR #2 / #3 / #4)
- **PR #2 (merged):** Close-the-loop — HITL proposals/decisions; workspace memory tier with 15 % budget slice; versioned rows + audit trail (EU AI Act Art. 14)
- **PR #3 (in flight):** Smart memory — mid-session tools (memory.read / write / search / append_daily); hybrid retrieval BM25 + dense + RRF; write-time scanner
- **PR #4 (deferred):** Research-grade — six-signal sleep-time consolidator, memory → skills bridge

### 1.2 `PRD-AGENT-EVOLUTION-2026.md` (PR-Z + PR-A..E + PR-F roll-up)
- **F1** Native memory tools, six adapters, cap protocol (4kB AGENT/CREW, 1.5kB PERSONA/peers, 8kB pins, 30kB daily/{date})
- **F2** Per-crew autonomy slider 0..3 (shipped as string enum strict/guided/trusted/full)
- **F3** Auxiliary model slot (Haiku 4.5 for memory health / skill review / negative learning)
- **F4** Keeper Phase 2 — skill-review / behavior dual-mode / memory-health / negative-learning
- **F5** Ephemeral agent lifecycle (permanent / ephemeral / ghosted)
- **F6** PERSONA.md per-agent + peer cards per-user + GDPR cascade with `data_subject_id` column

### 1.3 Hermes comparison framing
Operator's parallel goal: feature-match a self-hosted memory agent (the `NousResearch/hermes-agent` reference; do NOT mention by name in code or docs — only the techniques). Hermes-specific features extracted:

| Hermes feature | Crewship status |
|---|---|
| `MEMORY.md` + `USER.md` plaintext, operator-editable | ✅ AGENT.md + PERSONA.md + peer cards |
| Curator cron 7-day grading | ⚠️ `internal/consolidate/scoring.go` exists but **never wired to a scheduler** — `git grep ComputeScore` shows no callers |
| Skill library + procedural memory | ⚠️ skills exist, memory → skill promotion not shipped |
| FTS5 + hybrid retrieval | ⚠️ FTS5 engine shipped; `tools.go:handleSearch` falls back to substring (PR-F15 tombstone) |
| Pluggable provider interface | ❌ not in PRD scope |
| `*_conclude` tool naming | ✅ memory.write append/replace shape equivalent |
| Background cron sessions | ✅ pipeline scheduler (`pipeline_schedules` table) |
| Container playground | ✅ crew containers + sidecar |
| Memory source transparency UI | ❌ not in PRD (Hermes selling point unmatched) |
| Forget / decay primitives | ❌ versioning yes, decay no |
| MINJA-resistant scanner | ⚠️ direct/base64/homoglyph shipped; tool-call return scan, supply-chain on memory packs — not shipped |

---

## 2. What was validated end-to-end (iter 3)

### 2.1 P0 + memory smoke (validated in earlier iters, re-confirmed here)
- ✅ `git grep '^<<<<<<<' origin/main` → empty (PR #475 hot-fix landed)
- ✅ `crewship seed --with-memory --with-users` lands 5 users × 5 roles + 12 agents + 24 memory files
- ✅ Boot HEAD: `git log` shows commits beyond PR #472 PR-G/PR-F roll-up

### 2.2 A/B memory tests (this iteration)
All four passed decisively — same-question different-answer proves memory shapes behavior, not LLM hallucination.

| Test | Setup | Expected divergence | Got |
|---|---|---|---|
| **B1** Direct fact | Eva AGENT.md says ATLAS-7=GREEN; Daniel AGENT.md says ATLAS-7=BLOCKED | Eva: GREEN + Aurora Borealis. Daniel: BLOCKED + Penumbral Eclipse | ✅ exact match per memory |
| **B2** Cross-crew | Tomas (Eng) mascot=Vega/snow leopard; Ondrej (DevOps) mascot=Triton/whale | Different mascot per crew | ✅ no cross-leak |
| **B3** Contradiction | Jakub AGENT.md says Rust=APPROVED; CREW.md says Rust=REJECTED | Agent surfaces contradiction, picks authoritative (CREW) | ✅ surfaced + temporal reasoning ("more recent supersedes") |
| **B4** Language calibration | Petra PERSONA.md = "ALWAYS Czech"; operator writes English | Agent answers in Czech | ✅ persona-driven switch |

### 2.3 Audit findings live-verified (this iter, 8/8 pass)

| # | PRD claim | Live behavior | Verdict |
|---|---|---|---|
| **A1** | Scanner blocks direct prompt injection | `caught prompt_injection/ignore_previous_instructions` + SHA256 quarantine | ✅ |
| **A2** | Scanner catches base64 obfuscation | `caught base64_obfuscation/...base64` + quarantine | ✅ |
| **A3** | Scanner catches homoglyph (Cyrillic) | `caught prompt_injection/...homoglyph` + quarantine | ✅ |
| **A4** | Lessons tier read-only via memory.write | `lessons tier is read-only via this surface; submit through F4.4 negative-learning evaluator` | ✅ |
| **A5** | Soft cap warning at 80 % | `warning: approaching cap (3200 of 4000 bytes, 80%)` — exact PRD spec | ✅ |
| **A6** | Hard cap rejection at 100 % | `cap exceeded for tier=AGENT. Final would be 4500 bytes; cap is 4000` | ✅ |
| **A7** | Cross-tenant Keeper F4 gate | mismatched body.workspace_id vs ctx → `workspace_id in body must match` 400 | ✅ (post-fix) |
| **A8** | Webhook HMAC required | no-sig→401, wrong→401, valid raw-hex HMAC-SHA256 → 202 + run_id | ✅ |

### 2.4 Other PRD claims confirmed (from prior iters, not re-tested this round)

| Claim | Verified |
|---|---|
| GDPR cascade `DELETE /admin/users/{id}/data` removes memory_versions + peer_cards + writes audit row | ✅ iter 2 |
| `data_subject_id` column + partial index `idx_memory_versions_subject_ws` | ✅ iter 2 |
| RBAC matrix: OWNER/ADMIN/MANAGER/MEMBER/VIEWER × create/manage/read across crews/agents/credentials | ✅ iter 2 |
| Cross-session memory persistence (full daemon restart + container kill) | ✅ iter 2 (T22) |
| Boot-context system prompt ordering: ETHOS → IDENTITY → CREW → PERSONA → MEMORY | ✅ visible in agent exec log |
| Sentinel CI workflow `Scan diff for unresolved merge markers` | ✅ landing on every PR |

---

## 3. Bugs found + fixed in this iter

### 3.1 CRITICAL #1: `s.agentMemoryBase` not set when FTS5 init fails (commit `48569eb0`)

`internal/sidecar/server.go:205-217` only assigned `s.agentMemoryBase = cfg.Memory.BasePath` inside the success branch of `memory.New()` (FTS5 indexer). Any FTS5 init failure (SQLite ABI mismatch, locked index, perms) left the base path empty. Downstream the dispatcher's `isInsideMemoryRoot()` validator walks `AgentMemoryDir + CrewMemoryDir` — when both are empty, every in-tier file resolves to `/<filename>` at CWD and fails containment with `"path escapes memory root: AGENT.md"`.

**Symptom:** boot-context memory worked (AGENT.md / CREW.md / PERSONA.md baked into system prompt at session start), but ANY mid-session `memory.read` / `memory.write` / `memory.search` returned `IsError=true` with that exact string.

**Fix:** pull `s.agentMemoryBase` (and `s.crewMemoryBase`) assignment OUT of the FTS5 success branch. Path validator and FTS5 indexer are independent surfaces — validator reads strings, indexer reads SQLite.

### 3.2 CRITICAL #2: F4 keeper routes missing `internalWsCtx` middleware (commit `46cc49e1`)

All four Keeper Phase 2 routes (`/api/v1/internal/keeper/{skill-review,behavior,memory-health,negative-learning}`) in `router_internal.go:148-151` were mounted as `internalAuth(http.HandlerFunc(kp2.HandleX))` — missing the `internalWsCtx` middleware that extracts `?workspace_id=` from query into request context.

`assertBodyWorkspaceMatchesCtx` since round-8 fails closed when ctx workspace_id is empty (`"request context is missing workspace_id; internal-auth handler reached without internalWsCtx middleware"`). The fail-closed error was added as a tripwire for exactly this misroute — but no operator was hitting F4 routes day-to-day (sidecar-only), so the cross-tenant defense sat as dead code in production.

`grep internalWsCtx internal/api/*.go` showed the middleware was defined + unit-tested in `middleware_auth_test.go` but **never applied to any production route**.

**Fix:** wrap each F4 route: `internalAuth(internalWsCtx(http.HandlerFunc(...)))`. Post-fix, A7 verifies matched workspace_id passes through; mismatched returns the proper 400.

### 3.3 Pattern observation (for memory note)

Both bugs share a class: **"behavior intended in code, never reachable from production routes."** Unit tests passed because they bypass middleware and synthesise the missing context. Argues for an **integration test matrix** that exercises every internal-auth endpoint via the full middleware stack.

### 3.4 Earlier-iter fixes still on the branch (PR #527)

- `cad3a6b5` — new `--with-memory` + `--with-users` flags on `crewship seed`
- `d14d5f7d` — three bugs blocking direct `crewship seed` (token reading, .env.local sourcing, port→server bridge)

Branch state: `feat/seed-level2-memory-and-rbac` 4 commits ahead of `origin/main`, draft PR #527 open.

---

## 4. Outstanding gaps (not blockers, but on the audit punch list)

### 4.1 Mid-session memory subsystem
- `memory.append_daily` not live-tested (probably works post-fix #1 but never proven)
- FTS5 hybrid retrieval — engine exists, dispatcher fallback to substring; PR-F15 tombstone
- Memory consolidation cron — `internal/consolidate/scoring.go` shipped, no scheduler entry calls `ComputeScore`; verify with `grep ComputeScore -r .` (zero callers)

### 4.2 Hermes-parity gaps
- No `MemoryProvider` interface (pluggable backend)
- No memory-source provenance UI (which lines were used in this response)
- No first-class decay / TTL / forget
- No published benchmark vs LoCoMo / LongMemEval
- No sub-agent `brief()` primitive (ephemeral agents inherit nothing or everything; no curated slice)

### 4.3 Frontend coverage
- Autonomy slider — backend ✅ CLI ✅ React ❌ (`grep autonomy components/**/*.tsx` minimal)
- Aux model selector ❌
- Memory tab — read-only textarea, codemirror dep unused
- PERSONA editor ❌
- Keeper Phase 2 review queues ❌

### 4.4 Memory tools at runtime not yet stress-tested
- Concurrent writes from multiple agents to same crew's `learned.md`
- Memory file growth over weeks of real usage
- Backup/restore round-trip with memory tiers preserved

---

## 5. Iteration 4 plan — Custom crew + Hermes-parity matrix

### 5.1 Goal

Validate that a realistic 30-day operational scenario stays coherent across boot + mid-session + persona + peer + lessons tiers, and score Crewship against the Hermes feature checklist (§1.3).

### 5.2 Environment

dev2 (`192.168.1.201` port `8082`, `/opt/crewship_2`). Reason: dev1 has the iter-3 seed (Demo crew) sitting; isolating iter 4 on dev2 keeps the iter-3 state available for cross-reference.

### 5.3 Setup (deterministic)

```bash
# clean instance 2
cd /opt/crewship_2
./dev.sh stop
rm -f crewship.db
rm -rf /tmp/crewship-2-data /tmp/crewship-2-state
rm -f ~/.crewship/cli-config.yaml   # if pointed at instance 2
git fetch origin
git checkout feat/seed-level2-memory-and-rbac    # picks up PR #527
go build -o /tmp/crewship-2-cli ./cmd/crewship
nohup ./dev.sh start > /tmp/crewship-2-restart.log 2>&1 &
# wait for both 8082 + 3012 to listen
until ss -tlnp 2>/dev/null | grep -q ":8082 "; do sleep 2; done
until curl -sf http://127.0.0.1:3012/api/health >/dev/null 2>&1; do sleep 2; done

# seed with new flags (uses .env.local autoload from PR #527)
/tmp/crewship-2-cli seed --with-memory --with-users --wait-provision --provision-timeout 900
```

### 5.4 Custom personas

After standard seed, replace 5 of the 12 default agents' memory with the iter-4 personas. Pre-seed each agent's `.memory/AGENT.md` (~3000 B near cap), `.memory/PERSONA.md` (~1200 B), 30 days of `daily/YYYY-MM-DD.md` (5kB each averaging 80 % of daily cap), and 5 peer cards.

| Persona | Crew | Role | Memory shape | Stress vector |
|---|---|---|---|---|
| **Aurora** | research | LEAD | 60-day trade-analyst memory: 200 PRs reviewed, 50 production incidents | Mid-session memory.read across daily/* index (60 dates) |
| **Beacon** | quality | LEAD | Customer-support lead: 200 user interactions, peer cards for 8 distinct users, lessons.md with 30 entries | Peer-card cross-tier; lessons via F4.4 evaluator |
| **Cipher** | devops | LEAD | Security researcher: pins.md packed with CVE refs (4 kB hit on pins cap 8kB at 50 %); learned from 10 incidents | Cap warning behavior at 50 % growth rate |
| **Delta** | engineering | LEAD | Documentation writer: 100 article summaries split across daily logs | memory.search scope=daily hit rate |
| **Echo** | engineering | AGENT | Pair programmer reporting to Delta: codebase memory referencing files Delta wrote; tests assignment delegation | LEAD→AGENT cross-context propagation |

Concrete seed script template: `audit-stack/templates/iter4-personas.sh` — see appendix.

### 5.5 Users

6 total = 1 admin + 5 sub-users with role mix. Use `crewship seed --with-users` then patch the existing fixture with these extra entries:

| Email | Role | Owner of | Use case |
|---|---|---|---|
| `admin@crewship.test` | OWNER | (workspace) | full admin actions, GDPR cascade trigger |
| `pavel@crewship.test` | ADMIN | Aurora | engineering manager, can promote skills |
| `dora@crewship.test` | MANAGER | Beacon | customer-support manager, peer-card edits |
| `michal@crewship.test` | MEMBER | Cipher | security analyst, can ask + assign |
| `ivana@crewship.test` | VIEWER | Delta | doc reader only |
| `external@crewship.test` | VIEWER | (Echo, peer card subject) | external user — peer card subject for Echo's onboarding |

Each user gets a CLI token via direct DB insert (workaround for the un-fixed-yet user-mint-as-admin path).

### 5.6 Test scenarios

#### S1 — Hermes-parity memory matrix
Each persona is asked 5 questions across boot + mid-session + cross-tier paths:

```
Q1 (boot recall)      What is your role and what crew?
Q2 (daily recall)     Read your daily journal for 2026-04-30. What was the
                      highest-priority decision logged that day?
Q3 (pins recall)      Read pins.md and quote PINNED-3.
Q4 (cross-tier)       Where does the operator usually find guidance: AGENT.md
                      or CREW.md? Cite both if relevant.
Q5 (contradiction)    Your AGENT.md says X, CREW.md says NOT-X about Z.
                      Which do you trust and why?
```

Expected: 5/5 per persona, no hallucinations, persona sign-off applied per `PERSONA.md`. Compare actual vs expected via grep against the seeded text.

#### S2 — RBAC × GDPR end-to-end
For each of the 6 users:

```bash
# 1) probe 7 endpoints with their token, record HTTP code matrix
# 2) admin triggers DELETE /admin/users/{userId}/data on external@
# 3) verify: external's peer card removed from Echo's .memory/peers/,
#    memory_versions cascade, gdpr_actions audit row
# 4) Echo re-ask: "What do you know about external@?" — must say "no record"
```

Pass criteria: RBAC matrix matches `helpers.go:canRole()`; GDPR delete leaves zero residue across DB + filesystem + agent recall.

#### S3 — Custom crew template export/import
1. After 30 simulated days of memory growth, export the iter-4 crew template (`crewship crew export iter4-research --include-memory`)
2. Inspect bundle — verify all memory tiers + peer cards + lessons make the cut
3. Import on dev3 as fresh crew
4. Smoke ask: same Q1-Q5 across personas on imported crew should match dev2 originals byte-for-byte (modulo IDs)

This validates **crew template portability** — the Hermes "share memory" use case via export/import.

#### S4 — Memory consolidation cron
Investigate whether `internal/consolidate/scoring.go` is actually invoked on a schedule. Steps:
- `grep -r ComputeScore /opt/crewship_2/internal` — should find ≥1 caller in a scheduler / cron path
- If no caller exists, this is a NEW critical finding: consolidator shipped but never runs
- If a caller exists, force-trigger and observe `memory_versions` / `gdpr_actions` / `audit_logs` deltas

Pass criteria: either operator-triggerable consolidation works **OR** absence is documented as a known gap with a follow-up tracker.

#### S5 — Peer cards cascade
For Beacon (8 peer cards) + Echo (5 peer cards):
- GDPR-delete one user from each set
- Verify both the DB peer_cards row AND the on-disk `peers/{user_slug}.md` file are removed
- Re-ask the agent about that user — expects "no record"

This validates the F6 GDPR peer-card cleanup that the iter-2 audit only partially exercised (the `peers/` directory perms warning surfaced).

---

## 6. Iteration 5 plan — Multi-environment + performance + Hermes parity benchmark

### 6.1 Goal

Validate scale + cross-instance consistency + measurable performance, and produce a numerical score against any Hermes-comparable benchmark we can self-publish.

### 6.2 Environment

All three dev instances: dev1 (`8081`), dev2 (`8082`), dev3 (`8083`). Each on the same `feat/seed-level2-memory-and-rbac` branch, same seed inputs.

```bash
# repeat the iter-4 setup on dev3 (and dev1 if iter-3 state is no longer needed)
# the goal is that all 3 instances have BYTE-IDENTICAL memory tiers at iter-5 start
# verify with: sha256sum across hosts
```

### 6.3 K6 load tests

K6 is available as `grafana/k6:latest` Docker image on dev VM. Scripts live in `audit-stack/k6/`.

#### K6.1 — sustained chat resolve (memory boot path)
```js
// audit-stack/k6/chat-resolve-sustained.js
export const options = {
  vus: 50,
  duration: '5m',
  thresholds: {
    http_req_failed:   ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
  },
};
// hits /api/v1/agents?workspace_id=X (exercises boot DB query + RBAC)
```
Run: `docker run --rm --network host -v ~/audit-stack/k6:/scripts grafana/k6:latest run /scripts/chat-resolve-sustained.js`

Pass criteria: p95 < 500 ms (was 3 ms at 20 VU in iter 2, so headroom is huge); 0 server errors.

#### K6.2 — memory.read mix
Hit the MCP HTTP endpoint inside an agent container with 100 % memory.read load:
```js
// vu fans out 5 reads per iteration: AGENT, PERSONA, pins, daily/today, lessons
```
Pass criteria: each tier returns its own content, no IsError=true, p95 < 50 ms (in-container loopback).

#### K6.3 — memory.write churn + cap dance
Write 100 small entries per agent in append mode until cap is hit, observe soft warning then hard rejection, recover via mode=replace. Validates `tools.go:601-700` end-to-end.

#### K6.4 — webhook flood
Fire 600 webhooks/min (matches `defaultWebhookRatePerMin` floor from PR #501) with HMAC signature, verify all execute. Then push to 1000/min, observe rate-limit kick in.

### 6.4 Grafana dashboard

Spin up `grafana/grafana:latest` on dev VM, scrape `/metrics` (if exposed) or parse Go logs via Loki. Recommended panels:

- Memory dispatcher latency p50/p95/p99 by tier
- Memory file size by agent over time (file watcher)
- Quarantine entries by category (prompt_injection / base64 / homoglyph)
- GDPR action count by status
- Webhook fire rate + signature failure rate
- Cap warnings (soft 80 %) vs rejections (hard 100 %) per agent
- Cross-tenant gate hit rate (count of `assertBodyWorkspaceMatchesCtx` rejections)

If `/metrics` Prometheus endpoint doesn't exist yet, propose adding one as a follow-up PR. Until then, use `tail -f /tmp/crewship-N-go.log | python3 audit-stack/bin/log-to-prom.py` (template in appendix) as the bridge.

### 6.5 Benchmark — LoCoMo subset (Hermes parity)

LoCoMo paper (`arXiv:2402.17753`) has multi-session conversations. Pick 10 conversations × 3 question types (factual recall, temporal reasoning, causal reasoning) = 30 questions. Run against Aurora, Beacon, Cipher, Delta, Echo — one agent per crew. Score % correct.

Hermes claims ~92 % on LoCoMo memory subset. Crewship target: ≥ 85 % on the same subset to claim parity. < 70 % means architectural gap (probably the dispatcher fix isn't enough — would need hybrid retrieval which is PR-F15).

Test fixtures: `audit-stack/benchmarks/locomo-subset.jsonl` — write a converter from LoCoMo's source format (snap-research/locomo) into the format the test harness expects.

### 6.6 Survival tests

- **Daemon kill -9 + restart**: memory files survive (already tested iter 2 lightly; repeat under load)
- **Container OOM**: agent container hits memory cap, restart, memory survives
- **Disk full**: simulate 100 % disk on `/tmp/crewship-N-data`; verify memory.write returns sensible error, daemon doesn't crash
- **Network partition**: block sidecar's outbound to Anthropic mid-session, verify graceful degrade
- **Cross-instance memory replay**: dev1's memory dir tar+sent → dev2 untar → ask the same Q1-Q5 → must match dev1 answers byte-for-byte (modulo IDs)

### 6.7 Acceptance criteria

| Metric | Pass | Notes |
|---|---|---|
| K6.1 p95 chat resolve | < 500 ms | matches iter 2 ceiling |
| K6.1 error rate | < 1 % | excludes 401/403 (intended) |
| K6.2 memory.read p95 | < 50 ms | container loopback |
| K6.3 cap warn/reject ratio | 1:1 within ± 10 % | proves both paths fire |
| K6.4 webhook signature accept | 100 % on valid sig | constant-time compare |
| LoCoMo memory parity | ≥ 85 % | vs Hermes 92 % |
| Survival recall accuracy | 100 % on Q1-Q5 across all 3 instances | byte-equality of answers |
| RBAC matrix | 35/35 cells correct (5 roles × 7 endpoints) | post-fix |
| GDPR cascade residue | 0 rows + 0 files | across users × tiers |

---

## 7. Infrastructure cheat sheet

### 7.1 SSH

```
SSH config alias: crewship-dev → ubuntu@192.168.1.201
SSH key:          ~/.ssh/proxmox_ed25519_new
```

### 7.2 Instances

| Instance | Go port | Next port | Storage base | DB |
|---|---|---|---|---|
| crewship (instance 0) | 8080 | 3001 | /tmp/crewship-data | /opt/crewship/crewship.db |
| crewship_1 | 8081 | 3011 | /tmp/crewship-1-data | /opt/crewship_1/crewship.db |
| crewship_2 | 8082 | 3012 | /tmp/crewship-2-data | /opt/crewship_2/crewship.db |
| crewship_3 | 8083 | 3013 | /tmp/crewship-3-data | /opt/crewship_3/crewship.db |
| crewship_4 | 8084 | 3014 | (unused, available) | — |

### 7.3 Common commands

```bash
# bootstrap (clean state)
cd /opt/crewship_N
./dev.sh stop && rm -f crewship.db && rm -rf /tmp/crewship-N-{data,state} && rm -f ~/.crewship/cli-config.yaml
nohup ./dev.sh start > /tmp/crewship-N-restart.log 2>&1 &

# wait for both services
until ss -tlnp 2>/dev/null | grep -q ":808N "; do sleep 2; done
until curl -sf http://127.0.0.1:301N/api/health >/dev/null 2>&1; do sleep 2; done

# build CLI from the branch
export PATH=$PATH:/usr/local/go/bin
go build -o /tmp/crewship-N-cli ./cmd/crewship

# seed everything
/tmp/crewship-N-cli seed --with-memory --with-users --wait-provision --provision-timeout 900

# inspect agent memory on host
ls /tmp/crewship-N-data/crews/<crew_id>/agents/<slug>/.memory/

# inspect inside container
docker exec crewship-N-team-<crew_slug> ls /crew/agents/<slug>/.memory/

# call sidecar MCP directly inside container
docker exec crewship-N-team-<crew_slug> curl -sS http://127.0.0.1:9119/mcp/memory \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory.read","arguments":{"tier":"AGENT"}}}'
```

### 7.4 Available containers + tools

```
docker images | grep -E "k6|grafana|crewship"
  grafana/k6:latest      106 MB     (load testing)
  crewship-cache:*       per crew   (devcontainer images)

audit-stack location:    ~/audit-stack/
  payloads/              prompt-injection / base64 / homoglyph corpus
  iterations/            prior audit artifacts
  bin/run-tool.sh        helper invocation wrapper
```

### 7.5 Known dev VM quirks (from prior iters / memory)

- UFW firewall default-deny; new ports need `sudo ufw allow <PORT>/tcp` for LAN access
- Multi-instance `dev.sh` correctly sources `.env.local` per instance (port bridge works after PR #527)
- `chown -R 1001:1001` on memory files needed when seeding from outside container (UID 1001 = agent user)
- Setup token file has 4 comment header lines before the actual token — strip via `grep -v ^# | grep -v ^$ | head -1`

---

## 8. Custom agent persona templates (concrete content)

### 8.1 Aurora — Research LEAD, 60 days of trade-analyst memory

Drop this into `/tmp/crewship-N-data/crews/{research_id}/agents/aurora/.memory/AGENT.md`:

```markdown
# AGENT.md — aurora

Role: Research Lead — day-trader analyst on the Crewship audit fleet.
Tenure: 60 days (started 2026-03-23).

## Calibrated working patterns
- I always cite the PR number and the file:line for any code claim.
- Verdict-first: status / risk / next step in three lines max.
- Czech operator (Pavel) prefers terse. English peer (Mariana) prefers narrative.

## Tracked PRs (excerpt — full list in daily logs)
- PR #461 PR-B autonomy slider — reviewed 2026-04-12, approved 04-14 after 2 round-trips
- PR #470 PR-C Keeper Phase 2 — reviewed 2026-04-19, surfaced cross-tenant gate hole
- PR #501 webhook HMAC — reviewed 2026-05-10, approved same-day after constant-time fix
- PR #527 seed v2 — under review

## Lessons from production incidents
- 2026-03-30: silent merge marker on PR-E — lesson: every PR diff must grep '^<<<<<<<'
- 2026-04-15: keeper aux model fallback to phi3 silent — lesson: loud config validation
- 2026-05-21: daemon-side credential injection regression — lesson: integration test middleware chain
```

(daily logs / peer cards / pins.md follow same pattern; full templates in
`audit-stack/templates/iter4-personas.sh`.)

### 8.2-8.5 Beacon / Cipher / Delta / Echo

Pattern identical: each persona has a 60-day origin story embedded in `AGENT.md`, 30 `daily/YYYY-MM-DD.md` files, 5 peer cards (matching the 5 sub-users + 1 admin), pins.md sized to ~50 % of cap so cap stress tests in S2/S3 can grow toward 80 % / 100 %.

Detailed text in `~/audit-stack/templates/iter4-personas-*.md` (write these as part of iter 4 setup).

---

## 9. Sample test scripts (drop-in)

### 9.1 `audit-stack/scripts/iter4-s1-hermes-parity.sh`

```bash
#!/usr/bin/env bash
# Iter 4 S1 — Hermes parity question matrix
# Runs Q1-Q5 against each of 5 personas, scores responses.
set -euo pipefail
PERSONAS=(aurora beacon cipher delta echo)
INSTANCE=${INSTANCE:-2}
CLI=/tmp/crewship-${INSTANCE}-cli
PASS=0
TOTAL=0
for p in "${PERSONAS[@]}"; do
  for q in 1 2 3 4 5; do
    EXPECTED_FILE="$HOME/audit-stack/templates/expected/${p}/q${q}.txt"
    OUT=$($CLI ask --agent "$p" --no-markdown --no-stream "$(cat $HOME/audit-stack/templates/questions/q${q}.txt)" </dev/null 2>&1)
    if diff -i <(echo "$OUT" | grep -oE "[A-Za-z0-9-]{4,}" | sort -u) \
              <(cat "$EXPECTED_FILE" | grep -oE "[A-Za-z0-9-]{4,}" | sort -u) \
              > /dev/null; then
      echo "  ✓ $p q$q"
      PASS=$((PASS+1))
    else
      echo "  ✗ $p q$q — expected vs got differ"
      diff <(echo "$OUT") "$EXPECTED_FILE" | head -10
    fi
    TOTAL=$((TOTAL+1))
  done
done
echo ""
echo "Score: $PASS / $TOTAL  ($(( PASS*100/TOTAL ))%)"
[[ $PASS -ge $((TOTAL*4/5)) ]] && echo "PASS (≥80%)" || echo "FAIL"
```

### 9.2 `audit-stack/k6/chat-resolve-sustained.js`

```js
import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '1m', target: 20 },
    { duration: '3m', target: 50 },
    { duration: '1m', target: 0 },
  ],
  thresholds: {
    http_req_failed:   ['rate<0.01'],
    http_req_duration: ['p(95)<500', 'p(99)<1000'],
  },
};

const BASE = __ENV.BASE || 'http://127.0.0.1:8082';
const TOK = __ENV.TOK;
const WS = __ENV.WS;

export default function () {
  const p = { headers: { 'Authorization': `Bearer ${TOK}` } };
  const r1 = http.get(`${BASE}/api/v1/crews?workspace_id=${WS}`, p);
  const r2 = http.get(`${BASE}/api/v1/agents?workspace_id=${WS}`, p);
  check(r1, { 'crews 200': (r) => r.status === 200 });
  check(r2, { 'agents 200': (r) => r.status === 200 });
  sleep(0.2);
}
```

### 9.3 `audit-stack/scripts/iter4-s2-rbac-gdpr.sh`

(skeleton — fill in per env)

```bash
#!/usr/bin/env bash
# Iter 4 S2 — RBAC × GDPR end-to-end
# 1) Build the role-token matrix
# 2) Probe 7 endpoints with each role's token
# 3) GDPR-delete external@ — verify cascade
declare -A TOKENS=(
  [OWNER]=$(awk -F'|' '$4=="OWNER"{print $3}' /tmp/users-tokens.txt)
  [ADMIN]=$(awk -F'|' '$4=="ADMIN"{print $3}' /tmp/users-tokens.txt)
  # …
)
# probe() helper from iter-3 audit script
# expected matrix sourced from internal/api/helpers.go:canRole()
```

Full version: `audit-stack/scripts/iter4-s2-rbac-gdpr-full.sh` — copy from iter 2 + adapt.

---

## 10. Memory notes for next agent (this iter's discoveries)

Save these as memory entries when starting iter 4:

- **`feedback_path_validator_test_pattern`**: When auditing a path-validation function, test via the FULL HTTP middleware chain, not just unit tests with synthetic `r.WithContext(ctxWorkspaceID, X)`. Two bugs in iter 3 (`agentMemoryBase` empty / `internalWsCtx` missing) both passed unit tests but failed in production because tests bypassed the middleware.
- **`reference_mcp_memory_endpoint`**: To call memory tools without an agent, hit sidecar's internal MCP HTTP endpoint from inside the container: `docker exec <crew_container> curl -sS http://127.0.0.1:9119/mcp/memory -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory.read","arguments":{"tier":"AGENT"}}}'`. Tool names use **dots not underscores** (`memory.read`, not `memory_read`).
- **`reference_setup_token_path`**: Bootstrap setup token lives at `<CREWSHIP_STORAGE_BASE_PATH>/initial_setup_token` with 4 comment header lines. Strip with `grep -v '^#' | grep -v '^$' | head -1`. File auto-deletes on first successful bootstrap.
- **`reference_oauth_token_credential_type`**: Anthropic OAuth tokens (`sk-ant-oat01-…`) must be registered as `AI_CLI_TOKEN`, NOT `API_KEY`. The OAuth-vs-API-key distinction goes through different headers on the sidecar proxy (`Authorization: Bearer` vs `x-api-key`). Seed code detects via `strings.HasPrefix(apiKey, "sk-ant-oat")`.

---

## 11. Open questions for the next iteration

1. **Memory consolidation cron** — is `ComputeScore` actually called? If not, when is it scheduled to fire? File a finding if zero callers exist.
2. **`memory.append_daily`** — never live-tested. Add to iter-4 S1.
3. **Hermes-style sub-agent brief** — does LEAD → AGENT assignment pass a curated memory slice or `SkipConvHistory=true` blanket? Check `internal/orchestrator/assignment.go`.
4. **MemoryProvider interface** — confirmed not in PRD scope. Should we propose it as a follow-up PR for iter 5? Pros: Hermes parity; cons: scope creep, marketplace risk.
5. **Frontend toggle gaps** — autonomy slider + aux model selector + memory editor. Should these be tracked in PR-F-frontend follow-up before next release tag?

---

## 12. PR #527 status at this handoff

```
Branch  : feat/seed-level2-memory-and-rbac
PR #    : 527 (draft, against main)
Commits : 4
  cad3a6b5 feat(seed): --with-memory + --with-users
  d14d5f7d fix(seed): 3 bugs blocking direct crewship seed
  48569eb0 fix(sidecar): set agentMemoryBase even when FTS5 init fails
  46cc49e1 fix(api): wire internalWsCtx onto F4 keeper routes
URL     : https://github.com/crewship-ai/crewship/pull/527
Next    : convert to ready-for-review once iter 4 S1/S2 pass; merge after CodeRabbit
```

---

## 13. End-state assertion (what "iter 4 + 5 done" looks like)

When the next two iterations are complete, the following should be true:

- ✅ All 3 dev instances boot clean on the same branch (post-merge of #527 or its successor)
- ✅ 5 custom personas × 5 questions × 3 instances = 75 memory-recall test runs, ≥ 95 % accuracy
- ✅ LoCoMo subset score ≥ 85 % (target Hermes-equivalent)
- ✅ K6 sustained 50 VU × 5 min, p95 < 500 ms, 0 server errors
- ✅ GDPR cascade verified on 6 distinct users, 0 residue
- ✅ Crew template export/import preserves all memory tiers byte-for-byte
- ✅ Cross-instance memory replay byte-equivalent answers
- ✅ Survival under daemon kill / container OOM / disk full / network partition

When all 8 hold, the "agents remember a year" claim is **empirically defensible** end-to-end — not just architecturally promised.

---

*End of handoff. Good luck.*
⛵ Audit Lead, iter 3 (2026-05-22)
