# Brief for a UI/UX agent working on one Crewship area

You are one of several agents improving Crewship's web UI in parallel. The
product is one thing; keep it one thing. Read `docs/ux/README.md` first and
follow it literally — it is the contract, not a suggestion. The dashboard
(`app/(dashboard)/page.tsx`, `components/features/dashboard/`) and onboarding
(`app/(onboarding)/onboarding/page.tsx`, `components/features/onboarding/`)
are the reference implementations; match their density, tone and motion.

## Your area

Assigned clusters (one agent per row; the rows share little code and much data):

| Cluster | Screens | Why together |
|---|---|---|
| A · Conversations | `/inbox-v2`, `/chat` | both are "decisions and dialogue"; the Guide, approvals and escalations flow between them |
| B · Work & execution | `/issues`, `/routines`, `/activity`, `/journal` | an issue becomes runs, runs become journal entries; one timeline |
| C · Fleet configuration | `/crews` (incl. agents, skills), `/credentials`, `/integrations` | everything a crew IS; credential gaps and tools show up in all three |
| D · Pages, settings, admin + integration | `/pages`, `/settings`, `/admin` and the review of A–C | the loosest coupling, plus someone has to keep the whole consistent |

Do not edit files outside your cluster except `components/ui/*` shared
primitives — and when you add or change one, say so in `docs/ux/PRIMITIVES.md`
in one line, so the others pick it up.

## Working rules (non-negotiable)

- Work in **your own git worktree and branch** (`git worktree add ../crewship-ux-<cluster> -b ux/<cluster>`),
  never in `/srv/crewship/crewship_3` itself — another session's uncommitted
  work lives there and `git stash` is shared across worktrees.
- **Never deploy to dev3 yourself.** Verify on a throwaway server (below);
  the integrator (cluster D) deploys one merged state to dev3.
- Claim an issue before the first commit (`scripts/claim-issue.sh <n>`), one
  issue per cluster, and release it in-thread when you stop.
- Test first. A pure derivation gets a Vitest; a defect gets a red test before
  the fix. Docs ship with the change.
- Keep the memory of what you learned in `docs/ux/audit-<area>.md`, not only in
  chat: the next agent on this area starts from that file.

## Verification (the same for everyone)

```bash
# build once
pnpm build && ./scripts/embed-web-out.sh sync && go build -o /tmp/crewship-shot ./cmd/crewship
# empty DB, no docker, no live token probe
export CREWSHIP_STORAGE_BASE_PATH=$D CREWSHIP_LOG_PATH=$D/logs CREWSHIP_BOLT_PATH=$D/state.db \
  DATABASE_URL=file:$D/crewship.db CREWSHIP_NEXTJS_URL=http://localhost:8099 CREWSHIP_SKIP_SIDECAR=1 \
  CREWSHIP_E2E_SKIP_TOKEN_PROBE=1 CREWSHIP_PORT=8099 NEXTAUTH_SECRET=$(openssl rand -hex 32) \
  ENCRYPTION_KEY=$(openssl rand -hex 32) CREWSHIP_ALLOWED_ORIGINS=http://localhost:8099
/tmp/crewship-shot start --no-docker --db file:$D/crewship.db &
CREWSHIP_SERVER=http://localhost:8099 /tmp/crewship-shot seed --server http://localhost:8099   # demo@crewship.ai / password123
```

Then Playwright (import from the repo's `node_modules/playwright/index.mjs`):
log in, screenshot your screens at 1440, 820 and 390, and also after creating
100 of your main object through the CLI (`crewship crew create`,
`crewship issue create`, …). Report horizontal overflow as a number, not an
impression.

## What to deliver per screen

1. `docs/ux/audit-<area>.md` — purpose, dead ends (§6), missing cross-links
   (§5), inconsistencies (§2), each with a screenshot reference.
2. The fix, in the contract's order: dead ends → cross-links → anatomy → motion.
3. Before/after screenshots at three widths in the PR description.
4. One line per shared primitive you touched, in `docs/ux/PRIMITIVES.md`.

## Known findings to start from (2026-09-03, demo data)

- **Inbox v2**: an empty inbox is two blank panes ("Nothing here" / "Select an
  item"). It should say what lands here (approvals, escalations, failed runs,
  missed schedules), link to where those come from, and show the last resolved
  items so the page is never blank. Items lack the crew and the object that
  raised them as links.
- **Chat**: the breadcrumb leaks the internal slug `_crewship-setup-guide`;
  "NOT STARTED YET" is a row of unlabeled avatars; the empty conversation has
  no context about the agent (crew, role, model, what it can do).
- **Crews** with 100 crews: the left list has no grouping, no result count and
  no "needs attention" ordering; the right pane is a flat agent table.
- **Issues** is the strongest screen; align others to its density and board.
- **Routines** overview is already close to the dashboard's language; keep it.

## Implementation protocol (from 2026-09-03 — READ BEFORE THE FIRST EDIT)

Four sessions share `/srv/crewship/crewship_3`, and dev3 runs whatever that
tree has checked out. These rules exist so that four writers do not destroy
each other's work or knock dev3 over.

**Issues and branches**

| Who | Issue | Branch | Base | Throwaway port |
|---|---|---|---|---|
| Integrator (crewship-3-90) | #2300 wave 0, #2304 cluster D | `onboarding-client-redesign` → `ux/integration` | main | 8099 |
| crewship-3-28 | #2301 cluster A | `ux/a` | `onboarding-client-redesign` | 8094 |
| crewship-3-42 | #2302 cluster B | `ux/b` | `onboarding-client-redesign` | 8097 |
| crewship-3-fa | #2303 cluster C | `ux/c` | `onboarding-client-redesign` | 8098 |

1. `scripts/claim-issue.sh <n>` before the first commit; release in-thread when you stop.
2. `git fetch origin && git worktree add /srv/crewship/ux-<a|b|c> -b ux/<a|b|c> origin/onboarding-client-redesign`
   — work ONLY in that worktree. Never `checkout`, `stash`, `reset` or `commit` in
   `/srv/crewship/crewship_3`. (`git stash` is shared across worktrees.)
3. Rebase on `origin/onboarding-client-redesign` daily: wave-0 primitives land
   there (status pill, entity links, inline empty, paged list). Use them; do not
   write a local status map or a local empty state.
4. Backend first, in its own commit(s): the S1/S2 API changes your cluster needs
   (count + limit, DTO slugs, payload metadata). Every new endpoint or field
   gets a CLI command/flag and a Go test that drives the CLI binary; run the
   `internal/api` suite with `-timeout 25m`.
5. Then the UI, in the contract's order: dead ends → cross-links → anatomy →
   motion. Test first: a Vitest per pure derivation, a red test before every
   fix. `pnpm exec tsc --noEmit`, `pnpm exec eslint <your dirs>`, `pnpm exec
   vitest run <your dirs>` green before every push.
6. Verification on YOUR throwaway server and port (recipe above; rebuild with
   `pnpm build && ./scripts/embed-web-out.sh sync && go build -o /tmp/crewship-<c> ./cmd/crewship`
   — at most one `pnpm build` at a time on this box: check `pgrep -f "next build"`
   and wait). Screenshots at 1440/820/390 before/after in the PR.
7. **Never reload `crewship-ws@3`, never run `dev.sh` in clone 3, never touch
   dev1/dev2/stage.** The integrator merges `ux/a|b|c` into `ux/integration`,
   tests the combination and deploys dev3 from it.
8. One PR per cluster against `main`, opened when the cluster's P1 list is
   done: description = audit link, canvas link, before/after screenshots, what
   was and was not machine-reviewed. Wait for CodeRabbit (2–5 min); if it is
   rate-limited, review yourself to the same standard and say so. Never merge
   on red CI; the integrator merges.
9. Docs ship with the change: update `docs/guides/<area>.mdx` and add one line
   per shared primitive you touched to `docs/ux/PRIMITIVES.md`.
10. Report to crewship-3-90 at: backend done, UI P1 done, PR open. Do not ask
    questions the contract already answers; note assumptions in the PR.
