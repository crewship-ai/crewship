# Changelog fragments

Each PR writes its release note here as its own file instead of at the top of
`CHANGELOG.md`'s `## [Unreleased]` section. Every PR editing that one spot made
any two queued PRs conflict there (#4751); separate files cannot.

- **Name:** `changelog.d/<PR-number>-<short-slug>.md`, for example
  `changelog.d/4751-changelog-fragments.md`. Use the issue number if the PR
  number is not known yet. Top-level `*.md` files only; this README is not a
  fragment.
- **Content:** exactly the bullet(s) you would have added under
  `## [Unreleased]` — the **user-visible symptom in bold**, then what was wrong
  and what changed; `⚠️ **Behaviour change:**` when something that used to work
  can now fail. No heading.
- **Guard:** the Changelog Guard accepts a new non-empty fragment in place of
  an `## [Unreleased]` edit. An empty or whitespace-only fragment does not
  count, and editing or deleting an existing fragment is not a release note.
- **Release:** `python3 scripts/changelog-collect.py` moves every fragment to
  the top of `## [Unreleased]` (highest number first) and deletes the files —
  see `RELEASING.md`.
