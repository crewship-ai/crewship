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
