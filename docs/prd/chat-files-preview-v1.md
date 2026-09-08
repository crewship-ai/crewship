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
