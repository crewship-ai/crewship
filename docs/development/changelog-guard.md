<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## Changelog entries

`RELEASING.md` cuts release notes from `CHANGELOG.md`'s
`## [Unreleased]` section. That makes a missing entry not untidiness but
a release note that does not exist — and a change nobody is told about.

**A PR that touches `internal/api/`, `internal/orchestrator/`,
`internal/harbormaster/`, `cmd/crewship/`, `app/`, `components/`, `lib/`,
`hooks/` or `stores/` must add an entry under `## [Unreleased]`.** The
**Changelog Guard** workflow enforces it. The three frontend trees at the
end are peer top-level trees, not sub-directories of `app/` and
`components/`, and they carry as much user-visible behaviour: a chat or
socket fix lands in `hooks/use-chat.ts`, retry and error copy in
`lib/api-error.ts`. The two extra `internal/` packages are the agent's
runtime (what lands in its environment, how a run starts and stops) and
the approval gate a human reads; the rest of `internal/` is deliberately
not watched, because it reaches a user through the trees already listed. Test files inside any of those trees don't count as
user-visible, and Dependabot is exempt by actor.

**The entry has to be under `## [Unreleased]`, and the guard checks that**,
not merely that you touched the file. It compares that one section between
your base and your head, because `RELEASING.md` cuts release notes from it
and nowhere else — a typo fix in a shipped version's section, or a stray
blank line at the bottom, is not a release note and no longer passes. What
it still cannot judge is entry *quality*; `- fix bug` satisfies it. The
guard buys the reviewer that conversation, it does not replace them.

The guard's own logic is unit-tested by
`scripts/changelog-guard-test.sh`, which extracts the step's script
verbatim from the workflow and runs it against a throwaway repository —
including the case that matters most, a `git diff` that dies. That used
to be reported as an empty diff and a green check.

If the change genuinely has no user-visible effect — a chore, an
internal refactor, a test-only fix — apply the **`skip-changelog`**
label. The guard re-runs on `labeled`/`unlabeled`, so it clears within
seconds and no push is needed. Reach for the label when it is true, not
when the entry is inconvenient: the guard exists because 18 user-visible
PRs merged in one window with no trace anywhere — the #2086 audit found
fifteen of them and the backfill turned up three more — including a
breaking credential-model change across 117 files.

Write the entry the way the file already does — lead with the
**user-visible symptom in bold**, then what was actually wrong and what
changed. Mark a change that can break a working setup with
`⚠️ **Behaviour change:**` and say plainly what used to succeed and now
fails. Group under `### Added` / `### Changed` / `### Fixed` /
`### Security`.

`docs/changelog/overview.mdx` is a different surface with a different
job — a curated highlights reel for users, cut per release window, not
a per-PR log. It is deliberately **not** gated; see the scope note at
the top of the page. Adding to it is a release-time editorial call, not
a per-PR obligation.
