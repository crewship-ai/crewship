# Pages folders — delivery and acceptance status

As of 2026-09-14. Integration validation baseline: `bfa28fd8` on main,
also reported by dev3's running binary and frontend export. The binary
reports `vcs.modified=true`; the version check is not evidence of a
reproducible clean release artifact.

## Delivered scope

The collections analysis is historical. Its F3–F5 plan was replaced by
[the inherited-permissions model](pages-folder-permissions-linux-model-2026-09-13.md).
Delegated manage, grant expiry, grant-batch undo and nested folders are
excluded from that model, not unfinished features.

| Requirement | Implementation and evidence |
| --- | --- |
| Folder CRUD, icon/colour, one level, Unfiled, versioned membership | `pages_folders.go`, `pages_folders_test.go`; `pages-folders.test.tsx`; real CLI/router lifecycle in `acceptance_page_folders_test.go` |
| Sidebar grouping, search through folded groups, keyboard moves | `pages-rail.tsx`, `pages-rail.test.tsx`, `pages-folders.test.tsx` |
| A1–A7: read/write, management restrictions, human subjects, panel protection | `pages_folder_acl_test.go`; `pages_folder_acl_audit_test.go` explicitly rejects implicit agent reach and distinguishes people/crews |
| A8–A10: stale versions, atomic batch moves, revocation boundary | `pages_folder_acl_test.go`, `pages_folders_test.go`; authorized requests may finish after revocation, subsequent ones are refused |
| A11: bounded query count | `pages_list_reach_test.go` |
| A12: workspace page grants, never produce | `TestPageGrants_WorkspaceSubjectReadsAndWritesNeverProduces` |
| Effective access and caller-only paths | `pages_access_test.go`, `pages_folder_acl_test.go`, `acceptance_page_access_test.go` |
| Sharing, move impact, inherited access card | `folder-sharing.test.tsx`, `section-access-folder.test.tsx`, `lib/pages/__tests__/folder-sharing.test.ts` |
| API and CLI contracts | [API](../api-reference/pages.mdx), [CLI](../cli/page.mdx); CLI folder/share/move/access tests |

Folder ownership permits reading/administering the folder according to
role. It does not automatically give the owning crew access to every page
inside. Page access is the union of the existing page paths and explicit
folder ACL. The move preview describes what the folder adds and states
that existing page access remains.

## Review follow-up included with this record

The remaining refresh-error finding affected PagesLayout's open Sharing
dialog: a denied full-ACL request fell back to cached `shared=none` even
when refreshing the folder list failed. The dialog now requires an explicit
sharing marker from its caller. Both callers preserve folder identity but
use `unknown` when the list has failed. A component integration regression
covers private → failed refresh → recovery with a different audience,
without closing the dialog or issuing a write. The pre-fix test failed at
the expected privacy sentence.

Earlier fixes distinguish people from crews, deny implicit agent ACL,
qualify move impact, and render the selected folder colour on the icon.

## Verification boundaries

The baseline validation passed 1,000 frontend tests in 63 files, targeted
Pages API tests and CLI tests including real binary/router acceptance.
Relevant check-runs on that main commit, including CI Result, Go, API race,
Frontend and Playwright subset, succeeded; skipped checks were not counted
as executed verification. These are baseline results, not substitute results
for the follow-up patch's CI.

The live dev3 check used the demo administrator, opened Pages and Sharing,
closed the dialog with Escape, checked a coloured icon, and observed no
horizontal overflow on the overview at 360/768/1440 px or page errors.
No live grants or existing pages were changed during that validation.

Still not established by that check:

- a live two-user read/write/revoke walkthrough;
- every dialog at all three widths, a complete keyboard or screen-reader audit;
- the five-person / 90-second product measurement in the original analysis;
- a new live build/publish operation.

Automated role fixtures do not replace the first item, and automated UI
tests do not replace the user study. Delivery should not be described as
unconditional completion of every acceptance criterion until those checks
are separately recorded.
