# Pages editor — implementation evidence

What was built against [the v2 proposal](pages-settings-editor-review-proposal-2026-09-10.md),
what was measured, and what is still open. The proposal stays the design; the
[independent review](pages-settings-editor-independent-review-2026-09-10.md) stays
untouched as a historical document. This file is the record of the delivery and
is the only one that should be updated as it changes.

**Branch** `feat/pages-editor` · **base** `origin/main` `01d4849c` · issue #2491.

The proposal was verified against `01d4849c`, which is what `origin/main` was at
the time and still is. The clone's local `main` was 173 commits stale and its
checkout sat on an unrelated branch — neither is the baseline, and the work was
done in a separate worktree cut from `origin/main` itself.

## 1. P0

| §5 row | State | Where |
|---|---|---|
| Editor shell and navigation | Done | `components/features/pages/editor/page-editor-shell.tsx`, `use-editor-route.ts`; `/pages/<slug>?mode=edit&section=…&pane=preview` |
| Per-section capabilities | Done | `use-page-capabilities.ts`. The document-wide gate is gone; a sealed panel closes the document editor and nothing else |
| Content — ordinary panel Page | Done | `section-content.tsx`. Identity, address, facts, panels, one `Save changes` that says it writes the live Page |
| Content — review of an agent's change | Done | `application-review.tsx`, `review-preview.tsx` |
| Data & actions | Done | `section-data-actions.tsx`. Producer, last accepted, SLA, declared actions; no token form |
| Access | Done | `section-access.tsx`. Grants, producer tokens, public links, export, delete |
| History | Done | `section-history.tsx`. Three named restores with three different effects |
| Diff | Done | `lib/pages/definition-diff.ts`, `source-diff.ts`. No new dependency |
| Authorized review snapshot | Done | `GET /api/v1/pages/{slug}/project/review`, `?publication=N` for a retained version |
| Publish fence | Done | `expected_definition_digest` + `expected_routine_digests`, compared inside the publishing transaction |
| Author identity | Done | `actor_kind` on project history; the review shows the kind and id the server can prove, and no chat link |

Not built, and deliberately: a visual builder, duplication, autosave, an agent
inside the editor, executing actions in the preview. All P1 in the proposal.

## 2. What the toolbar's five buttons became

Every function reached by **Settings, Edit, App preview, Source history,
Publications** was accounted for before anything was deleted, and the two
dialogs and the settings modal were removed only once their assertions had been
re-homed onto the surfaces that replaced them.

One capability was **removed rather than moved**: the browser no longer calls
`POST .../project/check`. The review snapshot reports the same refusals as named
blockers before anyone consents, and publish re-runs the full candidate check
regardless. The endpoint and `crewship page project check` are unchanged.

## 3. Measured

Everything below was run; nothing is inferred from a passing neighbour.

| Check | Result |
|---|---|
| Vitest, Pages + `lib/pages` + the review hook | **816 passed / 53 files**, 0 failed, 0 skipped |
| `go test ./internal/api/ -run 'PageProject…'` | 14 top-level + 16 subtests pass |
| `go test ./cmd/crewship/` (whole package, real build image) | pass, 0 skipped — includes `TestSeedPageAppLifecycle/publish` and `TestAcceptance_PageProjectGitHistoryRestore/restart` |
| `go vet`, `go build ./...`, `gofmt` | clean |
| `golangci-lint run` on the changed packages | 0 issues |
| `tsc --noEmit`, `eslint` | clean |
| `make docs-inventory:strict` | 655 operations, 898 CLI commands, every gate clean |
| `go run ./scripts/docs-surface-check` | clean |
| Live pass on a throwaway server, binary `051802b0` | 21 screens at 360 / 768 / 1440 CSS px: **0 px horizontal document overflow, 0 JavaScript errors** |

Three fences were **mutation-checked** rather than trusted: breaking
`expected_publication`, `expected_revision` and the `git_commit` match failed
exactly three tests; breaking five of the retained-publish fences failed exactly
five. The comparator's guard against an agent authoring its own change summary
was checked the same way.

## 4. What running it found that the tests did not

Worth recording, because each is a class of thing a green suite does not catch.

- **Every review announced a change to the document's `apiVersion`**, because
  the live-page mapping used a string the validator refuses. Both test suites
  had pinned the wrong value on both sides, so neither could see it. The
  reassurance "this candidate declares the same panels" was unreachable until
  it was fixed.
- **A candidate removing a panel's `on_failure` showed no derived change** —
  the declaration that turns a quietly stale panel into work for a human.
- **A first publication never opened the review at all.** `has_application`
  means "a publication exists", so a Page whose application had only ever been
  a draft reported no application. Found by running the editor against a
  server. `has_project` now says the other thing.
- **A candidate that drops a `call` action could never be published from the
  browser**: it sent one routine digest too many and the fence answered 409
  naming a routine nobody had moved, identically on every refetch.
- **Republishing after a withdrawal was refused**, because a draft identical to
  a withdrawn publication counted as already live. Caught by the acceptance
  suite, not by a unit test.
- **An interrupted build rendered as "Ready"** on the review screen.

## 5. Limits, stated

- **The publish fence covers the live definition and the candidate's routine
  digests.** `baseline_unavailable` — the retained source behind the live
  publication no longer reading back — blocks publishing in the review screen
  but is **not** enforced by the server, so a direct API or CLI caller is not
  stopped by it.
- **The CLI's ergonomic path is not a review step.** `publish` and `rollback`
  with no fence flags read the snapshot and send it in the same command, and
  print what they fenced on. The documentation says so plainly rather than
  implying a human read it.
- **The preview does not execute actions** and renders with available live
  data, so a new or changed panel may legitimately show an empty state. It is
  not a test of the actions.
- **A fractional-second SLA** (`"90.5s"`) round-trips through an integer on the
  wire and produces a false "SLA changed". It fails towards a false alarm, not
  a false reassurance.
- **`mayManageAccess` is the only capability lowered from a live signal.** The
  others stay optimistic and the server's refusal renders at the control, which
  is the rule the design sets — a refusal must be visible where the action was.

## 6. Not done, and not claimed

**The five-person usability measurement in §7 has not happened.** No task in
that table has been measured with anyone. An automated pass and an
implementer's impression are not that measurement and are not offered as one.
The proposal's technical acceptance criteria are covered by §3 above; the
product criteria are not.

Also unmeasured: contrast ratios, reduced motion, and real screen-reader
behaviour. Focus handling after entering and leaving the preview is implemented
and read, not asserted by a test.
