# Crewship Guide — persistent product specialist

Status: implemented foundation · 2026-08-22

## Outcome

The onboarding Setup Guide becomes a permanent Crewship Guide. It starts the
first-run conversation, keeps the same chat and history, and remains available
from Chat after onboarding. Its reserved `kind=setup` crew stays out of the
user's fleet list; the agent itself remains visible and addressable.

## Identity (the guide's soul)

Crewship's native identity layers are the authored system prompt plus the
canonical CLI discovery files (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, Cursor
rules and Factory `AGENTS.md`). Persistent learned facts live in `.memory/
AGENT.md`; tone overrides live in `.memory/PERSONA.md`. `SOUL.md` is an import
alias from OpenClaw, not a separate Crewship runtime tier.

The authored prompt in `internal/api/onboarding_setup_crew.go` is therefore the
versioned soul/source of truth. The orchestrator writes that assembled prompt
to every adapter's discovery file before a run. Memory is enabled so the guide
can retain workspace preferences without mixing them into its immutable
product contract.

The guide is Crewship-only. It knows and explains:

- crews and agent roles;
- queue/trigger-based Routines;
- producer-push Pages and their typed panels;
- integrations, credential slots and egress;
- all manifest `crewship/v1` kinds and their intended boundaries.

It uses the user's preferred language and does not ask for secret values in
chat.

## Capability truth

The guide gets the same native, sidecar-backed routine MCP server as other
agents: capability discovery, list, save-with-test-gate and run. Those are real
operations and their result is reported as such.

Pages and arbitrary multi-kind manifests currently have a complete YAML/CLI
contract, but no browser-confirmed agent apply transport. The guide may design,
validate conceptually and hand off reviewable YAML; it must not claim that YAML
was applied. Adding an agent-only direct write would violate the onboarding
security finding: a prompt is not an authorization boundary.

The next control-plane slice is a stored manifest proposal:

1. agent submits YAML to a server-side parser;
2. server stores the immutable document and computed plan;
3. Chat renders that stored plan, including deletes, egress and credentials;
4. a human applies by proposal id through their own session;
5. apply re-reads the stored document and uses the normal manifest REST paths,
   preserving RBAC, audit and notifications.

Until that slice exists, no prompt may advertise direct Page or general
manifest application.

### Reading the workspace: `workspace_overview` (2026-09-02)

The guide also has `workspace_overview`: one read-only call, forwarded by the
sidecar to `GET /api/v1/internal/workspace/overview` for the sidecar's own
workspace, that returns every crew with its agents, icons and models, the
routines, the pages, the count of open issues, and which credential providers
exist — names and statuses only, never a value. The prompt tells the guide to
call it before advising about existing state or naming a slug, and never while
proposing the onboarding crew. This is what "valid access to the Crewship API"
means for the guide: it reads through the same agent-scoped internal door as
every other in-container tool, not through a user session.

The onboarding proposal marker may also carry `crew_icon` and `crew_color`
(lib/crew-icons.ts names and palette ids, mirrored and validated in
`internal/api/crew_icons.go`); the card and the created crew take that look,
and an unknown value is dropped rather than stored.

## Demo patterns used as product knowledge

`cmd/crewship/seeddata/builtin/pages.yaml` is the Pages teaching reference: a
single operational overview combines status, metric, series, table and
narrative panels owned by different crews. `cmd/crewship/seeddata/routines.go`
is the Routine reference: explicit inputs, deterministic transforms where
possible, cost/egress declarations, validation and escalation rather than
unbounded autonomous loops.

These are patterns, not templates to copy blindly. The guide must adapt names,
owners, producers, SLAs, schedules and external domains to the user's actual
workflow.
