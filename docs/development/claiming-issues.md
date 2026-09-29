<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## Claiming an issue before you work it

Several agent sessions work this repo in parallel — ten at once is
normal, and there are ~40 worktrees under `.claude/worktrees/`. They all
push as the same GitHub account, so **the assignee field cannot tell
"taken by another session" from "that's me"**. It is not a lock.

What it costs when nobody claims: #1481 re-fixed what #1471 had already
fixed two hours earlier, from another session, better. It surfaced as a
merge conflict, after both sides had done the work.

### The convention

**Before your first commit on an issue, post a claim comment naming
clone + branch + UTC time. Release it in the same thread when you stop —
whether it shipped or not.**

```bash
scripts/claim-issue.sh 1488                 # checks first, then claims
scripts/claim-issue.sh 1488 --check         # read-only: who holds it?
scripts/claim-issue.sh --list               # every open claim in the repo
scripts/claim-issue.sh 1488 --release "hypothesis unconfirmed, see above"
```

Claiming *checks before it posts*. If another clone or branch holds the
issue it prints the claim and exits **3** without commenting, so you find
out before the work, not at the merge conflict. `--dry-run` prints the
comment and posts nothing; `--force` overrides a refusal (say why in the
thread first). Exit codes: `0` clear or claimed · `2` usage error · `3`
held by another session.

The comment shape is plain text and hand-writable — the script only fills
in what you would otherwise mistype:

```
**CLAIM** — clone `crewship_3` · branch `fix/schedule-editor-save` · 2026-07-30T20:58Z
**RELEASE** — clone `crewship_3` · branch `fix/schedule-editor-save` · 2026-07-30T22:10Z
```

A release ends the claim(s) it names by **clone alone** (#2107) — branch is
recorded for readability but is not part of the match, because a claim is
posted before the feature branch exists (see below) and would otherwise
never be released from the branch that replaced it. A hand-written release
that names a branch but no clone still ends that branch's claims and leaves
the rest — global cancellation is reserved for a release that names neither
field, which is what "released it" means when someone types it without
ceremony.

**Release even when you failed.** #1482 was claimed, the hypothesis did
not hold, and the session said so and released — so the next one started
from evidence instead of re-deriving it. A dead end, written down, is
worth more than a silent unassign.

### When it goes wrong

- **A claim with nobody behind it (session died).** Claims older than 24h
  are reported as `STALE`. Stale still blocks `claim-issue.sh` — that is
  deliberate, because "old" and "abandoned" are not the same thing. Post
  in the thread that you are taking it over, then `--force`. Tune the
  threshold with `CLAIM_STALE_HOURS`.
- **No claim, but a branch or PR already exists.** `--check` also lists
  open PRs and local branches naming the issue number, because someone
  who got as far as pushing has effectively claimed it whether or not
  they commented. Treat that as held: ask in the thread first.
- **The claim is honest but the work was abandoned** — released with a
  reason, or claimed months ago with nothing pushed. The claim comment is
  a record, not a reservation: re-claim it, and say in the thread what
  you are picking up from the previous attempt.
- **The script guesses your identity wrong.** It reads the clone from the
  checkout path (`crewship_3`) and the branch from `git rev-parse`. A fresh
  `git worktree` (the normal way an agent session starts, before its first
  commit) checks out an auto-minted `worktree-agent-<hash>` branch; the
  script refuses that value on sight and falls back to the upstream branch
  if one is already tracked, otherwise the worktree path — no action needed
  from you. A detached-HEAD checkout (branch is the literal `HEAD`) or a
  container path with no `crewship_N` in it still guesses badly. Set
  `CLAIM_CLONE` / `CLAIM_BRANCH` to state it instead:

  ```bash
  CLAIM_CLONE=crewship_3 CLAIM_BRANCH=fix/aux-status scripts/claim-issue.sh 1488
  ```

- **You claimed only part of the issue.** Say so in the claim comment.
  A claim that names its scope ("items 1–2, not the Keeper panel") lets
  another session take the rest instead of the whole thing stalling.

The discipline that costs nothing and saves the most: **grep the issue
tracker before starting, not after the merge conflict.**
