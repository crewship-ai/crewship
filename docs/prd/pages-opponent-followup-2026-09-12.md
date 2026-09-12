# Pages opponent follow-up — 2026-09-12

Follow-up to the independent review of merge `917143e2` / PR #2492. Base of
this correction: `a0a5b3cb`. The review summary and the opponent's scratch probe
were available; the linked [review artifact](https://claude.ai/code/artifact/8a040cec-d7ab-4f28-9692-1ddea4888fce) could not be fetched. #2502 was
reopened because the prior claim that authoring paths were covered was too broad.

## Findings and corrections

| Finding | Correction / evidence |
| --- | --- |
| F1: PATCH/live rollback bypass panel visibility | Panel-list PATCH checks current and replacement owners; rollback checks current and archived owners. Neutral 403, no mutation. History uses `mayEditDocument` for live Restore. |
| F1 extension: metadata response exposed declarations | Metadata-only PATCH remains allowed as PRD requires, but returns the standard sealed placeholders. Hidden reference validation errors are neutral. |
| F1 extension: interleaved replacement | Both live writes CAS against the exact stored spec they read. A concurrently added hidden panel survives; stale panel replacement, metadata save and rollback return 409 before reconciliation/history insertion. |
| F2: project/check routine names | Filter only the wire report through `pageAuthorizedDocument`; preserve the full candidate report for publication provenance. Admins retain the complete map. |
| F3: stored-draft guard untested | New regression has fully visible live/replacement documents and a hidden panel only in the existing draft. Removing the stored-draft guard makes this test fail with a 200/revision increment. |
| F4: history absent before first publish | Render application history for a draft as well as an existing publication. Test exposes source restore before initial publication. |
| F5: baseline conflict discarded | Both client conflict decoders preserve `baseline`, tested for current and retained publication. |
| F6: source-only CLI absent | `page project get --source-only`, with optional `--revision`, reaches both source endpoints. CLI transport tests verify both URLs. |
| F7: diagnostic swaps / raised baseline | Add code-site anchors (statement + named declaration/test context); CI compares proposed baseline against target SHA. The initial anchor migration cannot grow the old file/code/message counts. Six Node tests and a real baseline-inflation rejection exercise the guard. |

## Scope and evidence

The initial regression run reproduced four legacy write violations and the
check routine disclosure. Both new history assertions failed before correction.
Targeted final CAS/security tests passed (4.275 s); frontend section/content/hook
regressions passed (60 tests); CLI source-only transport tests passed. Production
static build passed after installing dependencies inside this worktree (an
external node_modules symlink is rejected by Turbopack). ESLint: 0 errors,
31 warnings. Strict docs inventory: 657 operations / 898 CLI commands; OpenAPI
and inventory tests passed. Full Go/CI results are recorded on the follow-up PR,
not inferred here from targeted runs. No deployment is claimed.

The new gate retains 200 existing diagnostics; it does not fix #2493's debt.
Code-site anchors improve identity but do not prove semantic identity for two
indistinguishable diagnostics at the same statement. CI configuration itself
still requires review; this is not a defense against malicious modification of
the checking workflow.

## Product questions deliberately not silently changed

- Review consent for binary/truncated diffs needs an explicit policy on external
  review and the meaning of attestation. Existing warnings are not evidence that
  a human reviewed the omitted bytes.
- CLI auto-fetch of fences protects freshness from that fetch onward; it does
  not prove that a script/human inspected the same snapshot earlier.
- Shared-control contrast samples do not cover all custom editor boundaries.
  No whole-editor accessibility certification, screen-reader test or five-person
  usability study is claimed.

These are separate from the reproduced F1–F7 corrections. The prior merge is
not retrospectively called safe on every authoring path merely because its CI
was green; the post-merge regressions demonstrate the coverage gap.

The first full local Go run exposed two old public-link tests relying on blind
whole-document PATCH. The refused publication test now expects 403 and separately
seeds legacy attestation data to retain its public-link defense coverage. The
rename test now sends metadata only, matching the actual editor contract. These
and the security/CAS regressions pass together (5.481 s). Self-review also found
an anchor collision across named arrow helpers/classes; the anchor now includes
enclosing named declarations, with red/green assertions for both cases. The
regenerated baseline still contains 200 diagnostics and passes against main.

CodeRabbit's first substantive review identified the same missing function-like
scope anchoring, plus missing status-table rows and missing request context on
the extracted spec reader. Those are corrected; the reader now honors canceled
contexts, covered by a regression test. The review artifact URL is retained for
provenance. Expanded targeted project/security/public-link/cancellation tests
passed (36.297 s), and strict documentation inventory passed again.

Final self-review reproduced another response-side race: an administrator can
add a hidden panel after rollback commits but before its response loads panels.
The rollback response now uses the same viewer filter as PATCH. A deterministic
interleaving regression first exposed the full hidden declaration in a 200
response; it now requires only the sealed placeholder.
