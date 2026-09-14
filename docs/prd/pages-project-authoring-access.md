# Page project authoring access — #2502

The existing panel-visibility rule remains authoritative: write authority does
not grant visibility of panels owned by other crews. Whole-document reads and
replacements require full visibility. This is the separate-authoring-path
option recommended by the independent review, implemented during the user-
authorized takeover of PR #2492.

The source review endpoints omit the definition entirely. They preserve the
existing permission to read shared application source, which is arbitrary
Page-wide code and cannot be safely redacted by matching panel names. This is
not a claim that arbitrary source code is free of author-supplied references.

Tests: `TestProjectAuthoringRequiresEveryPanel` covers full reads, export,
source-only evidence, and prevention of destructive partial saves;
`TestProjectRevisionAuthorizationUsesArchivedOwner` covers archive ownership.
The existing review/publish authorization and fence tests remain relevant.

A future partial-document editor would need its own merge-on-write or sealed-
placeholder contract. Returning a filtered authoring document is not supported.

## Post-merge opponent corrections (2026-09-12)

The initial implementation missed legacy PATCH/rollback and the check response.
The follow-up requires full visibility of current and target documents for panel
replacement and live rollback. Metadata-only PATCH remains allowed, returns
sealed placeholders, and refuses hidden-reference validation failures neutrally.
Both legacy writes compare the original stored spec inside their SQL UPDATE;
a concurrent change returns 409 before panel reconciliation or version insertion.

`project/check` exposes only the visible routine digest map; full validation and
publication provenance remain complete. History disables live restoration for
partial readers and exposes source history before the first publication.
`page project get --source-only [--revision N]` reads either source-only endpoint.

The new hidden-draft-only save regression fails when the stored-draft authorization
guard is removed. Earlier tests checked the live document first and did not prove
that separate guard. These corrections supersede the earlier blanket claim that
all authoring paths were covered by #2502.
