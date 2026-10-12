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

## Verification boundary

Exercise the acceptance scenarios with an owned disposable workspace and
synthetic users. Internal instance checks and account observations are retained
in private context. A seeded demo is not evidence of production readiness.

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
