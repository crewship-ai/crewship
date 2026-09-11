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
| Vitest — Pages, `lib/pages`, the review hook and the navigation guard | **854 passed / 56 files**, 0 failed, 0 skipped |
| `go test ./internal/api/` — whole package | ok, 1088 s |
| `go test ./cmd/crewship/` (whole package, real build image) | pass, 0 skipped — includes `TestSeedPageAppLifecycle/publish` and `TestAcceptance_PageProjectGitHistoryRestore/restart` |
| `go vet`, `go build ./...`, `gofmt` | clean |
| `golangci-lint run` on the changed packages | 0 issues |
| `tsc --noEmit`, `eslint` | clean |
| `make docs-inventory:strict` | 655 operations, 898 CLI commands, every gate clean |
| `go run ./scripts/docs-surface-check` | clean |
| Live pass on a throwaway server, binary `051802b0` | 21 screens at 360 / 768 / 1440 CSS px: **0 px horizontal document overflow, 0 JavaScript errors**. Taken before the counter-review fixes; the screens they change have not been re-photographed. |
| CI on `93246b89` | **31 green, 0 failed, 6 skipped**. The 31 includes the CodeRabbit status, so it is not 31 completed test jobs — `Go Race` needed one re-run after an unrelated `internal/consolidate` flake (branch touches nothing there; main green; local `-race` ×3 green on branch and main; rerun green). |

Numbers move as the branch does; the figures above are the head named at the
top of this file and nowhere else. Earlier counts quoted in the PR body and in
chat (816 / 821 / 822) were accurate when written and are superseded.

Fences were **mutation-checked** rather than trusted: breaking
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
- **Restoring a retained version could be refused the same way**, because that
  fence filtered on having a current hash rather than on the version declaring
  the routine. Found by a review asking why a test fixture was missing a field.

## 5. Limits, stated

- **The publish fence covers the live definition, the candidate's routine
  digests, and the readability of the baseline.** The last one was a UI-only
  gate until the review of this branch called it what it was (CWE-602). It is
  enforced server-side now, but as an explicit statement rather than a block:
  compaction legitimately reclaims old checkpoints, so refusing outright would
  trade an integrity gain for a workspace that can no longer publish anything.
  Publishing without a readable baseline needs
  `acknowledged_unavailable_baseline`, and the publication's receipt records
  `verified` / `unavailable_acknowledged` / `initial_publication` so it can be
  found afterwards. **The review screen never sends it** — the design says the
  alternative to a full review has to be approved by the product, not assumed,
  so from a review the refusal stands.
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

**Nothing type-checks the test files.** `tsconfig.json` excludes them, so no
`tsc --noEmit` in this repo — local or CI — has ever looked at a harness, and
Vitest transpiles without checking. On this branch that hid two harnesses
rendering a component without required props, and a fixture missing a required
field whose absence was masking a real fence bug. The Pages tests here were
checked under a scoped config and are clean; the repo-wide gap is #2493, with
its ~3186 mostly-jest-dom errors measured rather than guessed.

Also unmeasured: contrast ratios, reduced motion, and real screen-reader
behaviour. Focus handling after entering and leaving the preview is implemented
and read, not asserted by a test.

## 7. The dev3 seed: what it actually did

`crewship seed` was run against **dev3** by mistake — `CREWSHIP_SERVER` in the
environment loses to a per-directory profile override, so a command meant for a
throwaway instance went to the live dev clone and reported success while the
throwaway stayed empty. The counter-review asked for an inventory rather than a
cleanup. Here it is; **nothing was reverted, and nothing should be.**

Inventory, read back from dev3 on 2026-09-11:

| Object | Effect |
|---|---|
| Pages | **None created.** All eight were created 2026-09-08 (`custom-operations` 09-09). Seven had `updated_at` set to 2026-09-10T22:59:50Z — their specs were rewritten with the same seed definitions. `custom-operations` was not touched at all (`= app custom-operations: publication history retained`). |
| Panel data | 8 payloads pushed at 22:59:50–54. |
| Crews / agents | **None created.** `ops`, `quality`, `engineering` and `credential-lab-20260908` all predate the run. |
| Routine runs | **4 one-off runs started** at 22:59:53 — `docs-drift-audit`, `ci-nightly-triage`, `page-watch`, `site-replica-audit`. Each routine now has exactly two runs in its history: 2026-09-08 (pre-existing) and this one. The two that had *failed* on 09-08 completed this time. |
| Schedules | **None armed.** `pages-operations-sample` (2784 invocations, still producing — its data carries today's timestamps) is a pre-existing schedule from 09-08, not this run. |
| Crew provisioning | Triggered for the three existing crews, in the background. Compute spent; no objects created. |

So the blast radius is: seven Page specs rewritten to the definitions they
already had, eight panel payloads, four completed one-off runs, and one
provisioning pass. No new objects and no recurring trigger was left behind.

The trap itself is in `docs/prd/` only as this note; the operational lesson —
a throwaway needs its own profile, bootstrapped over HTTP and addressed with
`--profile` on every call, and `crewship whoami` read before believing any
output — belongs with the CLI docs rather than here.

## 8. The counter-review of 2026-09-11

[`pages-settings-editor-counter-review-2026-09-11.md`](pages-settings-editor-counter-review-2026-09-11.md)
found three gaps in the approval path itself. All three were reproduced against
the shipped code before being fixed — its three probes are in the suite now, as
`components/features/pages/editor/__tests__/counter-review-probes.test.tsx`, and
they were confirmed to fail on the pristine `93246b89`.

- **R1 — a new digest could be approved over an old comparison.** The change
  list came from the Page detail query while the fence attested to the review
  endpoint's digest. When another author moved the live definition the two
  disagreed for as long as the detail lagged, consent reset, and the reader
  ticked it again while still looking at the old comparison. Resetting was
  never going to be enough. The review endpoint now carries **both**
  documents — live and candidate — authorized for the viewer and read in one
  handler from one read, and the screen derives its comparison from them.
  Correspondence holds by construction; there is no second query to disagree
  with. The wire-to-document mapping that existed only to bridge the two is
  deleted, and the fractional-SLA false alarm went with it.
- **R2 — consent worked before the sources arrived and after they failed.**
  One predicate now says whether every piece of evidence the decision rests on
  has arrived; until it does, consent is not offered and Publish is disabled,
  each naming what is missing. A failed read is an error with a retry, not an
  endless "Reading…", and a standing consent is cleared rather than merely
  disabled.
- **R3 — every link out of the editor bypassed the unsaved-work guard.** Only
  the workspace switcher consulted it; the global sidebar is plain `next/link`.
  The question is asked in the guard registry now, so it covers links this
  module has never heard of, and claims none of the clicks that mean "take me
  out of here" — a new tab, a download, another origin, a modified click.

A consequence worth stating, because it is a real loss and not a bug: withheld
panels are removed from **both** documents rather than stubbed, so no phantom
addition can appear — but a change confined to a withheld panel is invisible
here, including a panel re-pointed between a crew the reader may see and one
they may not. The screen says the comparison is partial and says which.

Still open from that review: nothing in R1–R3. Its two process points are
addressed — the numbers above are one head and one scope, and §5 no longer
describes the baseline check as client-side.
