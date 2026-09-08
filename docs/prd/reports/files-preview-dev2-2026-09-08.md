# Files preview — Dev2 verification, 2026-09-08

Scope: first manual PDF/image preview in Chat → Files, on the existing working
branch. No database migration, no changes to download authorization/endpoints,
and no sibling-instance deployment. See `../chat-files-preview-v1.md`.

## Checks

- 397 frontend tests passed across 36 chat/preview test files.
- Production static build and TypeScript passed; full ESLint had zero errors
  and 32 existing warnings. Go vet passed.
- Independent focused code review found no concrete blocker.
- After screenshot inspection, the narrow chat header was corrected to wrap
  trailing actions and retain a readable identity row. Ten focused tests passed.
- Live Dev2 acceptance passed all six scenarios with zero page errors or failed
  Files resources, including mobile PDF navigation at 390px and crew-route
  preview. [Live report](files-preview-dev2-live.md),
  [structured evidence](files-preview-dev2-live.json).
- A final cosmetic change hides the explorer count while previewing; all 22
  Files integration tests passed afterward.
- Complete `go test ./... -count=1 -timeout=30m` passed with exit 0:
  all 133 test-bearing packages succeeded, including API/database.
- Final standard Dev2 reload passed; screenshot-only recapture confirmed the
  compact header and absence of the explorer footer during preview.

## Live evidence

Normal Emma login on Dev2, real authenticated file endpoints and native PDF.js
canvas/image rendering. Synthetic PDF (two different pages), PNG and TypeScript
fixtures were uploaded through the supported owner CLI into Mařena's unique
`preview-demo-2026-09-08-fc52510a` folder. These are labelled QA samples, not
model-generated outputs. Test scripts: `e2e/prepare-files-preview-demo.mjs` and
`e2e/files-preview-live.mjs`.

The initial run verified PDF page navigation/zoom, exact-byte PDF download,
PNG native decode and the existing code editor. The screenshot exposed an
over-wrapped header beside the preview; that layout was fixed before final
capture.

## Existing limitation uncovered

Crew file save/list with `--path shared` exposes a separate shared-volume tree,
whereas the current Chat Crew root lists the output tree containing agent
directories. A file saved into that shared volume is not discoverable from the
current Chat Crew root. This preview increment preserves the existing explorer
and route scopes; it does not claim to fix shared-volume discovery. The later
Files explorer iteration should unify these roots explicitly. Previewing an
agent-output file through the Crew tree exercises the crew download route,
but is not evidence that shared-volume discovery works.

Auto-open on generation, output versioning, workspace-wide file search and
interactive HTML previews remain outside this increment. Preview fetches are
limited to 20 MiB; larger downloads use the existing browser-session download
route (bearer-only clients may require CLI download).
