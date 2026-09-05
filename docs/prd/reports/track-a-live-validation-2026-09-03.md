# Track A live validation on dev1 — 2026-09-03/04

Environment: dev1 factory-reset (`./dev.sh nuke --yes`) and seeded (`./dev.sh seed`) on `main` ecc62b514, so every row below is seed data or created by these steps — there is no real traffic on this instance. CLI: `/tmp/crewship-1-dev --profile dev1`, user demo@crewship.ai. Full command/answer log kept in the validating session's scratchpad (not checked in — it contains the seeded instance's ids and one webhook URL).

| Package | Live result | Evidence |
|---|---|---|
| A3 #2271 | PASS | `automation create --event nonsense.never_fires` → 400 naming registered types |
| A4 #2282 | PASS | 3 signed fires at a disabled routine → 3 `pipeline.webhook.fire_failed` journal entries + inbox item `webhook_fire_failed` (high) |
| A7 #2296 | PASS (one user) | `inbox read` → item leaves `--state unread`, appears in `--state read`; second-user side not provable with one account |
| A10 #2297 | PASS | `issue start` without delegate → 400 "Issue must have an agent delegate before starting"; after `issue update --assignee riley` the API returns `delegate={Riley}`, `owner` untouched |
| A2 #2279 | PASS | mention in the link form → `mentioned` activity and a run listed by `issue runs OPS-10` |
| A9 #2269 | PASS | second `issue start` for the same agent → `QUEUED (agent_busy)` beside one RUNNING; first run not killed, finished normally |
| A1 #2295 | PASS on RUNNING, FAIL on QUEUED → fixed in #2317, re-checked live: PASS | Stop of the running issue → run landed CANCELLED, summary preserved. Stop of the issue whose run was QUEUED → issue CANCELLED but the run started 35 s later and landed COMPLETED → #2312, fix PR opened |
| — | FAIL (new) → fixed | a mention-dispatched run on a BACKLOG issue could not be stopped: 400 "must be IN_PROGRESS or REVIEW" → #2315, fixed in #2320, re-checked live |
| — | GAP (new) → fixed | bare `@slug` in a CLI comment is not a mention (link form only) and the CLI could not produce the link form; `issue get -f json` lacked owner/delegate; runs API/CLI lacked mission_id → #2313, fixed in #2321, re-checked live |

Gates on `main`: `docker-api-surface` in sync (32 endpoints); `docs-inventory -strict` clean (588 API operations, 829 CLI commands).

## Re-checks after fixes

- **#2317 (Stop stamps QUEUED rows)** on `main` c3b0de420: two issues delegated to riley, both started, second parked `QUEUED (agent_busy)`; `issue stop` on it → run stayed QUEUED and landed **CANCELLED** when the first run ended, never RUNNING. (The first run itself ended FAILED on its own — an agent-side error unrelated to Stop; noted, not a Stop finding.)
- **A7 second user**: this seed run created no extra users (`workspace member list` shows only demo; `member1@crewship.local` login refused) and an invited member gets no password the CLI could use, so the two-user side stays **recorded as a limit, not proven**.

- **#2320 (Stop reaches a mention run on a never-started issue)** on `main` 1da7d149e: mention in link form on a BACKLOG issue → run RUNNING; `issue stop` → 200 "Stop requested"; run landed **CANCELLED** at t+30 s, issue status still BACKLOG.

- **#2321 (CLI gaps)** on `main` 6bf0ce178: `issue comment OPS-15 --mention riley …` → `mentioned` activity and a run; `issue runs -f json` shows `mission_id` and `source: mention`, the table has a SOURCE column; `issue get` shows `Owner: -` / `Delegate: Riley` (JSON `delegate={id,name}`); `--mention nobody` fails with "agent not found: nobody. Available: riley, morgan, …" and posts nothing.

## Dogfood (§20 step 2) — started 2026-09-04

- Routine `issue-triage-daily` (agent_run, ops lead `morgan`): reads the board with the CLI and posts a triage comment on the oldest open issue. Imported via `crewship routine import --crew ops`, hand-run once: `pipeline.run.completed` in 1m19s and the comment landed on OPS-1 ("Ops Board Triage — 2026-09-04").
- Schedule `psched_cmtmpyikl0001a69b5c8b`: `0 8 * * *` UTC, catch-up `skip`, next fire 2026-09-05 08:00 UTC — the first scheduled fire on a working clone is itself a §2.1 data point; check `crewship routine schedules list --slug issue-triage-daily` after it.
- Track B packages B1–B13 exist as ENG-7…ENG-19 on the dev1 board (crew engineering), so the 1.1 work is tracked in the product.
- Rows on dev1 are therefore of three kinds now: seed (`./dev.sh seed`), validation (OPS-8…OPS-13, R1–R3, a4-live webhook), dogfood (issue-triage-daily and its comments, ENG-7…19). The B0 re-audit of §2.1 must say which is which.

## First scheduled fire on a working clone — 2026-09-05 08:00 UTC

`issue-triage-daily`'s schedule (`0 8 * * *`) fired on time: run `run_cmto3f3fk…`, trigger `schedule`, 1 m 19 s, $0.15, `pipeline.run.completed`, and the triage comment landed on OPS-1 at 08:01:44Z. That is the first routine ever to run from a schedule on a working clone (§2.1 said nothing had). Its **outcome read `FAILED` — "no outcome reported"**: the prompt predates B6 and never asked the agent for an `outcome:` line, so B6's default applied exactly as specified. The routine's prompt now ends with the outcome instruction (new version saved through B8's `routine save`); the next fire is the check that the default no longer bites. Two footguns found on the way: a re-save without `--description` blanks the stored description (#2373), and a `--draft` save raises two inbox cards (noted on #2364).

## Track B landed — 2026-09-05

B1–B13 are in `main` (#2336, #2338, #2342, #2348, #2353, #2358, #2363 + #2366, #2367, #2372, #2378, #2377, #2380, #2383), each merged only with a green full suite and green CI, each reloaded onto dev1 and checked live through the CLI; the per-package results and their honest caveats are on the §17 lines and on each issue's RELEASE comment. Defects the live checks found and fixed on the way: #2312, #2315, #2313 (Track A), #2365 (B7's host-pid signal), #2381 (no `--force` door in the CLI), #2373 (a re-save blanking the description); test flakes filed: #2360 (fixed, #2361), #2386. Open after Track B: #2357 (agents' shell cannot reach the sidecar's issue verbs on dev1 — the read/reply half of the loop is proven only through the CLI), #2283/#2284/#2285/#2286/#2287 from Track A, and the B7 caveat that termination-within-5-s is proven against the fake provider only.

## Track B: what was reviewed, what is proven, what is switched on — 2026-09-05

"Track B is in `main`" is not "1.1 is done". §24 makes 1.1 conditional on all fifteen §18 scenarios green for two weeks and on the §19.3 series being inside their targets; neither has happened yet. The three tables below say exactly where each package stands.

### Pre-merge review, per PR

Every PR had three gates: the implementing agent's own `/code-review` pass (a separate read-only reviewer context, but directed by the same agent — not independent in the strict sense), a hand review by the coordinating session posted on the PR, and CodeRabbit where it actually ran (it was rate-limited on most PRs and said so on each). "Found / fixed" counts the review findings the PR thread records; "at merge" are the defects the coordinator's full suite, CI or live check caught after the review.

| PR | Package | Agent `/code-review` (found / fixed) | CodeRabbit | Coordinator review | Caught at merge / live |
|---|---|---|---|---|---|
| #2353 | B5 checkpoints + context pack | 2 / 2 (founding-run checkpoint instruction; unscrubbed secrets in replayed deltas) | rate-limited | yes | live: sidecar verbs unreachable from the agent shell → #2357 |
| #2358 | B6 outcome contract | 5 / 5 (`MarkTerminal` card source; `inbox_item_reads` lost in the CHECK rebuild — this one CodeRabbit found; case-insensitive parsing; self-reported CANCELLED refused; idempotent terminal row) | ran | yes | CI: five hand-built `pipeline_runs` fixtures lacked `outcome`; spend-test midnight flake → #2360/#2361 |
| #2363 | B7 hard termination | 2 / 2 (hard stop before exec start → `PENDING_EXEC`; missing binary-driven `--hard` test) | rate-limited | yes (§16.1 stated by the coordinator) | live: signal aimed at a host pid → #2365 |
| #2366 | B7b tmux-session signal | 1 / 1 (fast `NOT_FOUND` for runs without a session) | rate-limited | yes | live: termination time not observable (agents end in ~15 s) — caveat on #2365 |
| #2367 | B8 atomic authoring | 8 candidates / 4 fixed, 4 refuted with reasons (duplicate schedule on re-save; PATCH bypassing draft approval; typed `ErrInvalidTrigger`; CAS on activate) | rate-limited | yes | CI: gofmt ×2, route manifest, `lint-tsformat` comment; live: two cards per draft save → folded into B10 |
| #2372 | B9 reliability editor | 6 / 6 (rate-limit 0 dropped; double lookup; missing CLI flags; stale copy; stale-preview race; widened TS type) | rate-limited | yes | CI: required `cron_expr` in the OpenAPI verified list; CodeQL high `go/uncontrolled-allocation-size` |
| #2378 | B10 attention contract | 8 candidates / 5 fixed, 2 documented as prevented by `_txlock=immediate` (pipeline-run twin not threaded; unbounded merge; duplicated SQL ×2; redundant payload) | rate-limited | yes | — |
| #2377 | B11 board + parent/child | 4 / 4 (gap detector vs shared seq; paging past the 500-row cap; check-then-act on the terminal-children rule; cursor advanced before resync) — CodeRabbit contributed | ran | yes | CI: gofmt, stale `openapi.gen.json`; live: no `--force` in the CLI → #2381/#2382 |
| #2380 | B12 instrumentation | 1 / 1 (inverted PromQL example) | rate-limited | yes | CI: macOS `internal/memory` fsnotify flake (rerun green) |
| #2383 | B13 DONE/COMPLETED | 1 / 1 (`mission-control-bar.tsx` still read the retired word) | rate-limited | yes | CI: macOS `TestGolden12_Catchup_AllThreePolicies` off by one → #2386 |

Where the table says "rate-limited", no machine review happened and the PR says so; the hand review and the agent's reviewer are what stood in for it. That is weaker than the two independent reviews B1–B3 received and is stated here as such.

### The fifteen §18 scenarios

PASS = proven by a named test and, where possible, observed live on dev1; LIMIT = proven by a test but not observable live here; NOT PROVEN = no test or live record establishes it yet.

| # | Scenario | Status | Evidence |
|---|---|---|---|
| 1 | Mention an idle agent → ack < 1 s, one session, one run | PASS | `TestMentions_DeliveryAckedBeforeDispatch`, `TestMentions_SecondMentionReusesTheSameSession`; live: OPS-16/OPS-21; `crewshipd_delivery_ack_latency_seconds` p50 0 s on dev1 |
| 2 | Ten duplicate deliveries → one run | PASS (test) | `TestDeliveries_TenConcurrentIdenticalDeliveriesProduceOneRun`; not driven live |
| 3 | Follow-up during an active run → no second run, consumed once | PASS | `delegation_limits_session_test.go`, `issue_session_followups_test.go`; live: OPS-21 (one run, then one follow-up run) |
| 4 | Correction during an active run reflected in the next step | NOT PROVEN | B3 folds follow-ups into a new run after the current one ends; mid-run delivery is the F3 known limit — **#2350** |
| 5a | Stop → no further step, CANCELLED, late callback changes nothing | PASS | `TestIssue_Stop_*`, `TestRunAssignment_CancelRequested_*`; live: OPS-8, OPS-13 (after #2317), OPS-14 (after #2320) |
| 5b | Hard stop terminates the exec within 5 s, sibling unaffected | LIMIT | `TestIssue_Stop_Hard_TerminatesTargetNotSibling` (fake provider); live: sibling isolation held, termination time not observable (#2365 caveat) |
| 6 | Restart between event and consumption → nothing lost, no duplicate | PASS | `TestDeliveries_RestartBetweenEventAndConsumptionLosesNothing`; B4 live: restart mid-run, sweeper failed both orphaned runs, sessions → error |
| 7 | Resume after 7 days → no repeated work; pack bounded | PASS (test) | `TestAssembleContextPack_PackSizeBounded_DoesNotGrowWithThreadLength`, the 7-day wake test; live: checkpoint read back, `last_consumed_seq` 0 → 4, comment text unproven (#2357) |
| 8 | Parent with open child → no DONE without force; force writes a receipt | PASS | `issue_terminal_children_test.go`; live: OPS-31/32 (409, then the forced receipt) |
| 9 | Owner stays owner after delegating | PASS | `TestIssueDelegate_PreservesOwner_Scenario9`; live: OPS-8 |
| 10 | A peer agent's GO cannot satisfy a waitpoint | PASS (B14, #2388) | 2026-09-05 on dev1: `approval-gate-demo` parked on its gate; a POST to the approve route carrying an `X-Internal-Token` (the only credential an agent holds) and `{"approved":true,"comment":"GO"}` answered 401 and `routine waitpoints list` still showed the token; the inbox card carried `who_can_act: ["role:MANAGER"]`; `routine waitpoints approve` as the owner answered "Approved waitpoint" and the run resumed. The resolve door's own refusal — 403, row untouched, `waitpoint.decision_refused` on the audit log — is proven by the store and handler tests against the real store (on `main` the same handler test approved the waitpoint with 200), not live: no agent-facing route reaches the door, which is the point of putting the check in the door. |
| 11 | Schedule fires on time; wake gate false suppresses | PASS | `golden_schedule_test.go` (t1); live: `issue-triage-daily` fired 2026-09-05 08:00:29 UTC |
| 12 | Downtime over three fire times → catch-up honoured, all three variants | PASS (test) | `TestGolden12_Catchup_AllThreePolicies` (flaky at a minute boundary, #2386); not driven live |
| 13 | Duplicate webhook with the same idempotency key → one run | PASS (test) | `golden_webhook_test.go` (t1); not driven live |
| 14 | Routine authored → routine + trigger together, first fire stated, rollback proven | PASS | `TestPipelineInternalSave_Trigger_RollbackOnBadCron`; live: B8 draft save and bad-cron rollback |
| 15 | NEEDS_HUMAN → one item with an action contract; acting resumes the run, receipt, same thread | PASS (B15, #2389) | 2026-09-05 on dev1, `main` + B15: `issue comment OPS-34 --mention riley …` with a task that could not be finished without a decision → run `COMPLETED / NEEDS_HUMAN`, exactly one `run_needs_human` card (`attention_class: input`, actions `answer/take_over/dismiss`); `inbox act <card> answer --input "Use the staging bucket…"` → `delivered to the agent's session (dispatched), run cmtoko59x…; receipt seq 9`; `issue runs OPS-34` shows the new run RUNNING on the same session; `issue events OPS-34 --after-seq 0` has `inbox_acted` at seq 9 after the `mentioned` at seq 8; `inbox get` shows the SAME card resolved with `payload.receipt` (comment, delivery, run, seq) and the card count for the kind is still 1. Handler and CLI acceptance tests prove the same against the real store; on `main` the new tests do not compile (no act endpoint) and PATCH-resolving the card resumed nothing. Web inbox still renders no `actions[]` — #2398. |

Count: 9 PASS, 3 PASS-by-test-only, 1 LIMIT, 1 PARTIAL, 2 NOT PROVEN. The two-weeks-green clock of §24 has not started.

### Flags on dev1, and the §20 phases as a plan

`crewship feature-flag list` on dev1 (2026-09-05): `issue_agent_sessions` on (default, 100 %), `issue_deliveries` on (default, 100 %), `run_verdict_summaries` on. Nothing is in shadow mode: B1/B2 shipped their flags **default-on**, so dev1 went straight to the dogfood phase without the week of shadow comparison §20 step 1 describes — the comparison "would the new path have produced the same runs" was never run. **B3–B13 carry no flag at all, so they are on everywhere `main` is deployed — including `stage`, which is CD-owned and receives `main` automatically.** The canary on dev2/dev3 is therefore observation, not a switch, and the "default on" row below governs only the two B1/B2 flags; the rest cannot be turned off short of a revert.

| §20 phase | State | Plan |
|---|---|---|
| 1 Shadow | skipped | Not recoverable after the fact; the honest substitute is the live-check record above. |
| 2 Dogfood | running since 2026-09-04 on dev1 | Daily `issue-triage-daily` fire; Track B issues ENG-7…ENG-19 on the dev1 board. Keep for two weeks: until 2026-09-19. |
| 3 Canary | not started | dev2/dev3 after the dogfood window, one week: 2026-09-19 → 2026-09-26, with the §19.3 series read daily. |
| 4 Default on | not reached (B1/B2 flags only; B3–B13 have no flag) | Only once all fifteen scenarios are PASS (#2350, #2388, #2389 must be built first) and green for two weeks — earliest 2026-10-03, and only if the canary week is clean. |

### §19.3 — first reading, dev1, 2026-09-05 13:30 UTC

Read from `GET /metrics` on dev1 after the live checks (seed + validation traffic, ~20 deliveries). Targets are the PRD's; whether a value is "inside" is stated per row, and rows without a series say so.

| §19.3 question | Series (B12) | Value on dev1 | Inside target? |
|---|---|---|---|
| Delivery ack latency | `crewshipd_delivery_ack_latency_seconds` p50 / p95 (n = 19) | 0 s / 0 s | yes (target < 1 s) — but the sample is one instance's validation traffic |
| Claim latency (queue wait) | `crewshipd_delivery_claim_latency_seconds` p50 / p95 | 0 s / 19 s | p95 reflects the one queued-behind-a-busy-agent case; no target set for this row |
| Lost deliveries | `crewshipd_deliveries_lost` | 0 | yes |
| Duplicate active runs (I2 canary) | `crewshipd_duplicate_active_runs` | 0 | yes |
| Context pack size | `crewshipd_context_pack_tokens` p50 / p95 | 134 / 351 tokens | yes (bounded) |
| Checkpoint compliance | `crewshipd_session_runs_checkpointed_total` / `_finished_total` | 8 / 21 | **no** — 38 %; the seeded agents skip the checkpoint block on short runs |
| Scheduled-fire punctuality | no series (B12 left it to the scheduler) | one data point: 08:00:00 fire at 08:00:29 | not measurable yet |
| Inbox items per successful run | no series | — | not measurable yet |

Next reading planned 2026-09-12 (end of the first dogfood week), then weekly through the canary window; the two missing series need their own issue before default-on.
