# Chat artifact preview — analysis and proposed scope

Status: proposal, not implemented. Based on the current Dev2 working tree
(`fix/chat-workspace-foundations`, base `56969d7e`), source inspection and the
user's two screenshots. No new browser functional claims are made by this audit.

## Existing implementation

- `components/features/chat/right-panel.tsx` routes a file selection to the text
  editor or a download surface. `lib/file-format.ts` excludes PDF/raster images
  from previewable extensions.
- `components/features/chat/artifact/artifact-pane.tsx` already has tabs and an
  editor, but its only enabled view is editor. `artifact-file-io.ts` reads bytes
  with `Response.text()`, so this cannot serve as a binary document viewer.
- `components/ai-elements/artifact.tsx` uses a fixed overlay, not a resizable
  side-by-side layout. Chat mounts it alongside the existing Files drawer.
- `components/features/chat/assistant-turn.tsx` exposes Open in Artifact from
  file-tool inputs. That affordance is not proof that a completed, persisted
  user-facing output exists; automatic opening needs a stronger contract.
- `stores/artifact-store.ts` keys tabs by agent and path. That is insufficient
  identity for session-specific outputs and future shared conversations.
- `internal/api/proxy_files.go` and `internal/server/routes_files.go` return
  downloads as octet-stream/attachment. Keep this download contract intact:
  existing artifact IO deliberately rejects JSON responses as error/list data.
- Existing PRD `chat-as-a-primary-surface.md`, section 6.3, already anticipates
  image/text/PDF previews and requires a rendering-isolation decision.

## Proposed experience

One right-hand workspace panel should own both Files and the selected artifact.
Team remains an alternate panel; opening an artifact must not stack drawers.
Keep the rail label Files and use a user-facing Preview title for its viewer.

```text
 Chats             Conversation          │ report.pdf          ↓  ⛶  ×
 People / Agents   Agent: Report ready   │ Page 1 / 4   − 100% +
 Channels          [PDF report.pdf Open] │
                   Follow-up messages   │     document preview
                   Message composer     │
```

Desktop: adjustable split; preview initially takes about 50–60% of the content
area, with a readable chat minimum width. Collapse the chat-navigation sidebar
when space requires it, preserving its previous state when preview closes.
Mobile: full-screen preview with an explicit return to the same chat/scroll.

The message contains an output card with type icon, name, size, readiness/error
state, Open and Download. Clicking either this card or Files opens the same
viewer. Support tabs for multiple outputs, close, expand, and keyboard focus
return. PDF gets pages/zoom, images get fit/zoom, code gets highlighting/copy,
Markdown gets rendered/source views. Editing is a separate explicit action.

Auto-open only a verified completed output from the active conversation's
current request. Never auto-open on history load, reconnect, a background
routine or another conversation's activity. If someone is already reading a
different artifact, show a new-output indicator instead of replacing it.
Do not treat every code fence, log or intermediate tool file as an artifact.

## Implementation boundaries

1. Unify selection and layout before adding another viewer component. File
   references include workspace, source scope (agent/crew/conversation), owner,
   path or stable file ID, conversation/message/run reference, and revision.
   Cancel reads and clear visible bytes on scope switches.
2. Add type-aware binary loading and bounded viewers. Prefer a maintained
   PDF renderer with page virtualization; choose/version-check the actual
   library during implementation. Read text only for confirmed text formats.
   Use size/page limits and release object URLs/resources when closing tabs.
3. Define a durable published-output record plus a ready notification. Server
   verifies existence, authorization and completion before a card is ready.
   A plain tool-input path cannot be this source of truth. Publish a stable
   snapshot/revision so reruns cannot silently change an earlier report.
4. Preserve authenticated download routes. Authorize preview against the same
   resource on every fetch. Conversation membership must not implicitly expose
   the producer agent's whole filesystem; shared files require explicit scope.
5. Initial allowlist: PDF, PNG/JPEG/WebP, text/code/Markdown. Render code as text,
   sanitize Markdown, and do not execute generated HTML/JS/SVG in the app origin.
   Active web previews require a separate sandbox design. Unsupported formats
   get a clear Download fallback. Never infer safe content from extension alone.

SQLite can hold artifact metadata and revision references; bytes belong in the
existing file/storage layer. This feature does not itself require PostgreSQL
or a new service. Retention, backups and access control of shared snapshots need
explicit implementation; this is not a capacity certification of current infra.

## Delivery and acceptance

First deliver manual previews from existing files/cards: one panel, PDF/images/
text, responsive split and consistent download. Next deliver verified published
outputs, automatic opening, replay-safe readiness and session filtering. Then
shared-channel output permissions/revisions and links from routine/issue results.
Interactive HTML, Office rendering and collaborative editing are later scope.

Required scenarios: real agent-created PDF/image/code; successful open/download;
no premature open during write; two outputs in one answer; failed/missing/large
file; misleading extension; malformed PDF; HTML/script treated as inert content;
cross-workspace and unauthorized-channel denial; agent/session switches during
slow downloads; revision correctness; reload/history/reconnect without auto-open;
background completion without focus theft; keyboard/mobile; unsaved edit guard.

## Reference

The user-provided ChatGPT screenshot illustrates a persistent chat beside a
document viewer. Claude documents a dedicated right-side artifact window,
multiple artifacts, source inspection and downloads:
https://support.claude.com/en/articles/9487310-what-are-artifacts-and-how-do-i-use-them
The proposal borrows this interaction pattern, not their internal architecture.
