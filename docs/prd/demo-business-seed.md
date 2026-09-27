# Business demo seed

The default onboarding seed is now organized around four Harbor Goods projects:
Sales, Finance, Marketing and Shipping. Crewship Lab contains real workspace
telemetry and a bounded live container monitor. All demo copy is English.

The agreed goal is a short, inspectable customer story that also exercises real
Crewship functionality. Prepared Issues start TODO. Local scripts provide
predictable checks, Pages show their results, Issue comments record findings,
Inbox receives notifications and approval decisions, and a completed action
leaves an outbox receipt. AI drafting is optional and separately visible.

Old CI-watch, docs-drift, release and site-replication examples are removed from
the default workspace. Legacy implementation helpers and explicit regression
evals remain available in source; they are not silently installed or scheduled.
The default MCP integration reads local demo records. Demo vault examples are
inert SMTP/webhook entries; real provider authentication is configured separately.

See [the story package](../../cmd/crewship/seeddata/stories/business/README.md)
for the executable contracts, extension steps, verification commands and limits.

## Acceptance

- A clean seed creates 3 crews, 9 agents, 5 projects/Issues, 6 custom Pages and
  15 routines. No business routine executes during seed.
- Each Issue is assigned and linked to its check routine; Page actions expose
  check, optional AI draft and resolution separately.
- All four checks work without AI credentials, publish real script outputs,
  comment on the correct Issue and notify the triggering user's Inbox.
- Human approval gates the local Sales, Finance and Shipping delivery. Keeping a
  case open leaves it TODO; accepted completion changes the correct Issue to DONE.
- Repeated actions have one concurrency slot per story; local artifacts are unique.
- Live monitoring produces new measured values without running a routine for
  every sample, and stops on request or after 15 minutes.
- `seed verify` fails on missing files, failed runs, missing provenance or Inbox
  items; `--complete-demo` also checks final Issue status and local evidence.

This is not a claim to cover every Crewship feature. Live SaaS OAuth, real email
delivery, credential-request interactions and multi-agent delegation need their
own explicitly configured acceptance scenarios. The four default stories must
remain useful with local fixtures and without third-party accounts.

## Dev1 verification, 2026-09-25

- Complete disposable-workspace acceptance: **36 PASS / 0 FAIL**, including
  all four check/resolve flows, Page provenance, approval, DONE Issues and outbox
  receipts. The later verifier also checks assignment and Issue comments.
- CLI factory reset removed SQLite and runtime data; a fresh install and a
  non-destructive re-seed both completed. Final workspace: 3 crews, 9 agents,
  5 Issues (all TODO), 6 Pages, 5 matching folders, 15 routines.
- Final workspace check-only acceptance: **24 PASS / 0 FAIL**. No customer action
  was approved in the final workspace; Sales was exercised with Keep open.
- Browser: check action reached completion; reload and repeat of the resolution
  action reused the same pending approval; Keep open preserved TODO. All six
  Page apps rendered. Sales at 390px viewport had no horizontal content overflow.
- Real live samples arrived at five-second intervals; Stop halted publication.
- Codex drafts succeeded for Sales, Finance, Marketing and Shipping. Taylor
  actually called the `harbor-goods.read_demo_records` MCP tool and returned
  `3, L-103`. A structured-result parser regression found by that test was fixed
  so MCP envelopes are tool events, not raw assistant prose.
- `.env.local` checksum was unchanged. Real provider authentication and the
  one-panel callback files remain outside Git. Gitleaks found no staged leaks.

The developer's Codex subscription login was exercised successfully, but the
previous handoff reported a refresh-token 401. Working current access is not
proof that a copied subscription login will refresh indefinitely. Customers
supply their own provider credentials; the basic stories work without them.

## Critical acceptance follow-up

The audit found that the four AI drafts published correct Page data but their
final Page-write acknowledgement contained no structured run outcome. Technical
`completed` therefore coexisted with outcome `FAILED`. Draft routines now report
their result only after saving and publishing the draft. Missing saved work
fails; a completed case returns `NO_CHANGE` without invoking the model.

- A separate disposable workspace on dev1 exercised **all 15 routines**. Every
  routine had a persisted `completed` / `SUCCEEDED` run with no error message.
- The strengthened full verifier passed **48 checks**, including authoritative
  outcomes, Page provenance, Issue ownership/assignment/comments, Inbox decisions,
  DONE transitions and local receipts containing the original source finding.
- All **9 agents** answered the real provider smoke test successfully.
- Drafts were read for meaning, not just length: the audience is now explicit
  per story (customer, carrier or internal marketing diagnosis); shipping
  evidence includes EUR. Explicit draft delimiters keep agent progress commentary
  out of Pages and approval text; malformed drafts fail instead of being saved.
  Generated proposals are still human-reviewed text.
- The monitor advanced timestamps, stopped publishing, and the completed Finance
  draft path skipped the agent and reported `NO_CHANGE`.
- API/database suites passed with temporary test data in RAM after disk-backed
  runs timed out. The CLI/seeddata suites also passed.

The customer lesson is **check → understand the finding → optionally draft →
decide → inspect the receipt**. Finance completion records a reminder, not a
payment. Shipping completion records claim evidence, not a paid refund.
Marketing simulates local delivery; it does not prove a production email route.
These distinctions must stay visible when extending the catalogue.

## Visual identity and agent voices

Project metadata is the common palette for Issues, Page folders, Pages and
business routines: Sales uses cyan/handshake, Finance amber/wallet, Marketing
violet/megaphone, Shipping blue/truck and Crewship Lab cyan/activity. Routine
icons distinguish check (search), draft (sparkles) and resolution (badge-check).
The live controls use play/power. These are supported palette IDs, not hex
values that crew rendering would silently replace with its fallback colour.

Crews are named Sales & Shipping, Finance & Marketing and Operations, with
descriptions explaining their actual responsibilities. Stable slugs remain
unchanged so references and existing work survive a re-seed.

Each agent has a versioned `seeddata/souls/<slug>/SOUL.md` template. The seed
installs its contents into the supported agent `.memory/PERSONA.md` tier. It
preserves an existing agent persona; operator edits must survive re-seeding.
If the host persona writer cannot write container-owned memory, the existing
memory import API places the document inside the container and the seed verifies
the exact contents through the persona read API. Filesystem permissions and
environment settings are not changed.

Alex coordinates calmly; Sam is warm and customer-focused; Robin watches
deadlines; Jordan is precise and tactful; Casey is curious and experimental;
Morgan is steady under pressure; Riley explains measurements patiently; Taylor
questions evidence; Jamie is a constructive skeptic. All retain the same
honesty, credential handling and structured-output requirements.

Dev1 verification: all nine installed personas matched their source templates;
Sam and Jordan answered live with the expected distinct priorities. Browser
checks rendered Issues, Routines, Pages and Crews without JavaScript errors.
