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
