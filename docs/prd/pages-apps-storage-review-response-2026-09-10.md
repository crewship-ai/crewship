# Pages storage/compiler review response — 2026-09-10

Independent review scope: #2475 at `4563f5db`, 48 files. The reviewer reported F1–F4 and explicitly did not compile because the disk was full. CodeRabbit's older review is not fresh-head coverage.

## Fixes

Fix commit `79af8b5f` addresses all four findings:

- F1: bootstrap `expectedParent` uses browser-compatible default-port serialization, including IPv6 loopback; non-default ports remain intact.
- F2: invalid source/artifact contents are distinguishable from filesystem I/O failures. A validated replacement is installed atomically at the same digest. Quota accounting excludes the replaced slot and allows repair at the entry limit. Reads still reject corrupt data; permission/read failures still propagate.
- F3: successful worker exit does not bypass output-overflow validation. Both stdout and stderr are checked before decoding.
- F4: the source-layer README no longer links to files that only arrive in later layers. All remaining local link targets were checked to exist.

F1 and F2 regression tests failed before production changes. After the fix, `go test ./internal/pages ./internal/pagebuild -count=1` and focused vet passed. Removing F3's overflow check made its new regression fail with `invalid worker artifact: unexpected EOF`; restoring it passed. Tests additionally cover corrupt-slot recovery at the entry quota and preservation of an underlying filesystem read error.

## Integration

Integrated main `6ce5afd4` through source, server, CLI and UI. Server integration commit `d32f8e43` resolves the three conflicts semantically:

- The MCP list contains ten tools, including `page_project`, `get_routine_draft` and `save_routine_draft`.
- `go run ./cmd/gen-openapi` generates 654 operations. The docs test supplies the derived sentence: 630 named success schemas, 24 without a success body, and 281 request bodies (277 named JSON and four non-JSON).
- `go test ./internal/api -run TestMutationRouteRolesMatchManifest -update-route-roles` regenerates the manifest from the combined router.

Router manifest, MCP list and OpenAPI prose checks passed. The docs-layer changelog conflict preserves both the new routines entries and the Pages documentation entry.

[Automatic source PR CI](https://github.com/crewship-ai/crewship/actions/runs/34483084330) tests source head `6cc3e2d6e8190bec4682427d8a50e03f7237f449`.
[New cumulative UI dispatch](https://github.com/crewship-ai/crewship/actions/runs/34483523993) tests `c68722da3d8c5d45ef11f19609614b5b0373e5ae`.
The cumulative UI run completed successfully: 18 executed jobs passed and the conditional Binary Build job was skipped. The source PR run also completed successfully. Both runs were started after the integration. Historical successful runs at `08fcfc60` do not validate these changes. PR bodies carry the new links and this distinction.

## Operations and remaining acceptance

The regeneratable Go build cache was cleared after confirming the disk pressure. Available space rose from approximately 1.5 GB to 84 GB (then about 80 GB as builds resumed). No application data was removed. `crewship-ws@3` remained active and its health endpoint returned 200. This work does not claim a new deployed binary.

UI lint passed (zero errors) and the production static build passed after generating the worktree's missing Prisma types with `pnpm exec prisma generate`. Local `go vet ./...` passed. The local full Go attempt hit the default 10-minute
API timeout (no failed assertion), then continued into other packages. The
separate API retry and remaining local full run were stopped after both fresh CI
Go jobs passed, avoiding duplicate long suites with a cold cache. These local
attempts are not recorded as passing full runs; full-suite evidence is the CI
Go jobs on the exact source/UI heads above. All configured race jobs passed in the cumulative UI run; the source PR's separate general race job also passed. Layer review remains separate from CI. CodeRabbit was re-requested for #2475; throttling permits the documented manual fallback. Authoring from chat and clean production installation remain open acceptance gates. Safari remains panel-only for v1 as explicitly approved.

## Base moved before merge

Before merging #2475, main advanced to `6a9857f5` via #2482 (92 files).
The two successful runs above do not cover that newer base. No PR was merged.
The newer main is now integrated through the stack without additional conflicts.
Current code heads: source `39146cdb`, server `4122eb35`, CLI `4740b70b`, UI `832e69fc`.
Fresh [source PR CI](https://github.com/crewship-ai/crewship/actions/runs/34488138789)
and [cumulative UI CI](https://github.com/crewship-ai/crewship/actions/runs/34488234440)
are running. Neither is recorded as passed yet. The older green runs above remain
valid historical evidence, not a merge gate for these new heads.

## Dispatcher integration regression

The new routines dispatcher derives its idempotency key from `PendingRun.FireAt`,
but `DueRuns` did not load `fire_at`. The returned time was always zero, so rearming
the same one-time row could reuse the old execution identity. Server commit
`4b33e6ef` loads and parses the stored occurrence. A regression failed first with
`FireAt=0001-01-01`, then passed for initial dispatch and a rearmed row. The full
pipeline package passed (12.129s), as did its vet. Integrated Pages API tests also
passed (8.683s).

Current code heads after this fix: source `39146cdb`, server `4b33e6ef`, CLI
`6402d250`, UI `0bba090e`. The UI run above is superseded by
[fresh cumulative CI 34488992444](https://github.com/crewship-ai/crewship/actions/runs/34488992444)
at `0bba090e8008aa1e2f9222b9435c38c8fab1aa18`. It is running; source run
34488138789 remains the applicable source PR check. No PR has been merged.
