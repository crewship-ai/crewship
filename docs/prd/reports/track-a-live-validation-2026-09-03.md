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
