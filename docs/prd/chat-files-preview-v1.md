# Files preview v1

Implemented scope: manual previews in Chat → Files, using the existing agent
and crew file trees and authenticated download endpoints. This is the first
increment of `chat-artifact-preview-analysis-2026-09-08.md`.

## User flow

Click a file in either tree or a transcript file link. Text/code keeps the
existing editor. PDF, PNG, JPEG and WebP open a read-only preview occupying the
Files panel, with Back to files and Download. Unsupported binary formats show
a download fallback. On desktop the preview switches to the existing push
layout so the conversation remains usable, and Resize preview changes width;
the panel's draggable separator also remains available. Mobile uses the
existing Files screen.

PDF.js renders PDF pages to canvas, with previous/next page and zoom controls.
Raster files use a signature-checked blob image. File extensions do not grant
permission to execute HTML, SVG or JavaScript. Existing SVG/HTML code entries
continue through the text editor.

## Boundaries

- No new download/API contract or database migration. Both preview and editor
  preserve the agent/crew source scope; crew paths use crew endpoints.
- Preview fetches are bounded to 20 MiB, including chunked responses, and aborted
  on source changes. Blob URLs and PDF tasks are disposed on closing/switching.
- Chat panels remount on workspace/agent/session changes so stale file content
  cannot carry into another conversation. Unsaved text edits require the
  existing discard confirmation before changing files.
- PDF worker/support assets ship locally with the static build, not a CDN.
- Auto-opening freshly generated artifacts, durable output revisions, unified
  artifact/editor tabs and workspace-wide search remain future increments.
- Demo fixtures used for validation are synthetic labelled samples uploaded
  through the supported CLI, not claimed as real agent-generated artifacts.

## Validation

Component coverage includes bounded reads, signatures, load/retry/cleanup and
PDF controls. Integration coverage exercises agent and crew routes, transcript
entry, list return, workspace reset and cancelled unsaved-edit navigation.
Live Dev2 evidence and final build/check results are recorded in the associated
report after verification.

## Chat Artifacts preview increment (September 2026)

The agent-scoped Artifacts list includes PDF, HTML, CSV/TSV and images.
Markdown rendering is supported by the preview component, but Markdown is
excluded from the client Artifacts list. Opening an artifact uses a push pane beside chat. Its header matches
the Files workspace: document icon, title, full storage path and Download.
PDFs use that single header rather than repeating a second file toolbar.
The live revision strip remains visible while the pane follows changes.

Markdown opens as a formatted, script-free document in an opaque sandboxed
frame. CSV/TSV opens as a table. Managers can switch those text formats to the
existing source editor and save through the agent-scoped file route; readers
cannot enter edit mode. Editing pauses live follow so incoming revisions do
not overwrite the working copy. Native XLSX/DOCX editing is not implemented;
XLSX remains download-only. The Dev2 examples `demo-copy-site-plan.csv` and
`demo-copy-site-brief.md` were uploaded via the CLI as labelled sample data.

Creating an empty folder from Files needs its own agent-scoped API operation.
The current save route writes files and creates parent directories as a side
effect, but using a hidden placeholder file as a folder control would be a
misleading storage contract. Keep the control out of the UI until that API
exists and is covered by the same role and path checks as file save.

## Chat surface consolidation (September 25, 2026)

The client-facing Files tab has been removed from Chat. The right rail now has
Artifacts and Work only. Artifacts uses the agent-scoped download and save
routes, with the existing live polling and previewers. Its list conservatively
accepts PDF, HTML, CSV/TSV, XLS/XLSX and raster images outside run, attachment
and configuration folders. Markdown is currently excluded from the client
list, including the sample brief. This is a UI filter, not an authorization
boundary or proof that the agent authored every matching file; creation
provenance requires a separate backend contract.

Clicking an artifact opens a preview beside chat. Expand gives the preview the
main canvas, folds the left conversation list and restores the Artifacts list
on the right for switching outputs. Returning to chat preserves the selected
artifact. Transcript links use the same client-facing path filter, so an
AGENTS.md or run-scaffolding link cannot enter the former Files editor flow.
Agent configuration remains available on the agent card through its existing
management route. The obsolete file workspace code is retained as an internal
component for now, but Chat no longer exposes it.
