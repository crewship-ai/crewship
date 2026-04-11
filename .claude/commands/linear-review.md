---
description: Comprehensive Linear task review — done, open, blockers, relevance, stale detection
---

# Linear Task Review

Delegate Linear analysis to a subagent so the main context stays clean. Linear MCP responses are large; never load them directly into the main conversation.

## How to execute

Call the Agent tool with:

- `subagent_type`: `general-purpose`
- `model`: `haiku` (fast and cheap — this is pure data fetch + summarize)
- `description`: `Linear task review`
- `prompt`: the SUBAGENT PROMPT below, verbatim

After the subagent returns, present the findings to the user in Czech and offer three follow-ups: (1) deep-dive on a specific issue, (2) re-prioritize something, (3) close stale issues.

## SUBAGENT PROMPT

You are performing a comprehensive Linear review for the Crewship project. Use the `mcp__linear-server__*` tools. Return a concise structured report — no raw JSON, no filler.

### Project context

Crewship is a Go-based AI agent orchestration platform. Current strategic focus (April 2026):

- **Docker-first architecture** — Kubernetes, BYOE, and localproc runners are explicitly deferred or rejected
- **Foundation epic CRE-117** covers: restic backup engine, shared memory FTS5 fix, portable brain (`.crewship-brain`), security baseline (dedicated Docker network + credential health)
- **Open-core licensing**: Apache 2.0 core + `/ee` proprietary (BUSL-1.1 planned)
- **Monetization**: Free / Team ($29) / Enterprise ($149) tiers
- Team in Linear: **"Crewship"**
- Sub-issues of CRE-117: CRE-118, CRE-119, CRE-120, CRE-121

### Required queries (use narrow filters to save tokens)

1. **Recently completed** — last 14 days:
   `list_issues(team="Crewship", state="Done", updatedAt="-P14D", limit=30, orderBy="updatedAt")`
   If "Done" state name is wrong, try `state="Completed"` or list statuses first via `list_issue_statuses(team="Crewship")`.

2. **Open Urgent** — priority=1, not yet done:
   Fetch without state filter, then exclude terminal states (Done/Canceled/Duplicate) in post-processing:
   `list_issues(team="Crewship", limit=100, orderBy="updatedAt")` filtered by priority=1 client-side, OR
   `list_issues(team="Crewship", priority=1, limit=100)` if the priority filter is supported (it is — see tool schema).

3. **Open High** — priority=2, not yet done (same approach).

4. **Foundation epic deep-dive**:
   `get_issue(id="CRE-117")` — parent metadata
   `get_issue(id="CRE-118")`, `CRE-119`, `CRE-120`, `CRE-121` — each sub-issue's status, blockers, assignee

5. **In Progress** snapshot:
   `list_issues(team="Crewship", state="In Progress", limit=20)` — what's actively being worked

6. **Stale detection**: any issue with `priority ≤ 2` whose `updatedAt` is older than 60 days and status is still "Backlog" → flag as potentially stale.

### Analysis steps

1. **Done recently**: extract ID, title, 1-line outcome
2. **Foundation progress**: status + blocker + next action for CRE-117 through CRE-121
3. **Relevance categorization** of non-foundation open Urgent/High:
   - **A — Foundation-aligned**: directly supports backup, memory, security baseline
   - **B — Valid but deferrable**: real value, not current critical path (MCP polish, integrations, nice-to-haves)
   - **C — Potentially stale**: old, untouched, superseded, or misaligned with Docker-first strategy
4. **Blockers graph**: any explicit `blockedBy` or `blocks` relationships, flag circular deps
5. **Top 3 recommended next actions**: concrete, one sentence each

### Output format (STRICT — return exactly this structure)

```
## Linear Review — YYYY-MM-DD

### Recently completed (last 14 days) — N items
- **CRE-XXX** — title — 1-line impact
- ...

### Foundation Epic progress (CRE-117)
| ID | Title (short) | Status | Blocker | Next action |
|----|---------------|--------|---------|-------------|
| CRE-117 | Foundation epic | ... | — | — |
| CRE-118 | Shared memory FTS5 | Backlog | — | ... |
| CRE-119 | Restic backup | Backlog | — | ... |
| CRE-120 | Portable brain | Backlog | CRE-118 | ... |
| CRE-121 | Security baseline | Backlog | — | ... |

### In Progress (active work) — N items
- **CRE-XXX** — title — assignee — % progress (if known)

### Other open Urgent/High issues — top 10
| ID | Title (short) | Priority | Cat (A/B/C) | Rationale (1 line) |
|----|---------------|----------|-------------|--------------------|
| ... | ... | Urgent | A | ... |

### Potentially stale (flag for review) — N items
- **CRE-XXX** — title — why flagged (last updated, reason, etc.)

### Blockers graph
- CRE-XXX → blocks → CRE-YYY
- ...

### Recommended next 3 actions
1. **[concrete action]** — 1-line rationale
2. **[concrete action]** — 1-line rationale
3. **[concrete action]** — 1-line rationale
```

### Hard rules

- NEVER dump raw JSON or full descriptions
- NEVER list more than 10 items in any section (truncate with "+N more")
- Use short Linear IDs (CRE-XXX), never repeat full titles
- If a query returns empty, say "— none" explicitly
- **Total output under 600 words**
- No introductory or concluding prose — just the structured report
- If Linear API returns unexpected errors, report them concisely and continue with what you have
