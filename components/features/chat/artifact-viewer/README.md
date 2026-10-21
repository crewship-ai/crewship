# Controlled saved Document viewer shell

This is partial split-view preparation for one verified saved Document in
explicit `latest` mode. It does not implement a Files library, historical card
revision resolution, network adapter, source registration, or shared layout.
The existing native-editor anchor remains outside this component.

`types.ts` defines a `ViewerTarget` with workspace and immutable Page identity.
That identity is a reference, not an access grant. The host integration must
resolve it through current authorized server sources and supply the title and
content together in an `AuthorizedDocumentView`. The shell invokes `renderBody`
only when that envelope exactly matches the requested scope and target. It
never fetches content or infers a private title from model text or a path.

The caller owns `scopeKey`: bind it to the current human, workspace, source
chat and authority generation. Change/invalidate it synchronously on any
identity or permission change, abort prior requests and discard late results.
Do not pass an old authorized response under a new scope. Definitive denial
must clear protected metadata/content; temporary failure is a distinct state.
The host’s real body renderer remains responsible for content schema validation
and appropriate read/edit permission. This shell grants neither.

The subtitle is deliberately **Current saved document**. A creation result's
revision 0 is not a pinned historical native snapshot. Historical rendering
needs its own real immutable revision reference and resolver before this target
union may expand. Files and other types are unsupported in this cut.

The shell exposes controlled close/expand callbacks, a mobile Back to chat
action, initial close-control focus, and Escape/close focus restoration to a
still-connected opener only after the controlled target becomes null. Reopening
cancels pending restoration; an ignored close callback never moves focus. It does not trap focus or declare a modal: the host
owns desktop split/mobile placement. Escape already consumed by a nested
control does not close the viewer. Keep the shell mounted with `target: null` to observe that closed transition.
A parent unmounting or closing the shell independently must handle its own
focus restoration.

Keep the live transcript, composer, pending attachments and editor mounted at
their existing identities when opening, expanding or closing the viewer. The
host owns layout integration and permission handling. This component contains
no store, WebSocket, send/dispatch, editor state, permission mutation, data fetch
or restoration path. `renderBody` must remain a pure renderer of the supplied
authorized content.

Focused checks:

```sh
crewship-heavy pnpm exec vitest run components/features/chat/artifact-viewer/document-viewer.test.tsx
```

Props fixtures verify shell behavior only. Real resolver/Document rendering,
CLI and browser acceptance remain incomplete until integrated and executed.
No complete H1, linked-only library, sharing or exact historical-revision pass
is claimed by this component.

## Current saved Document resolver

`useDocumentViewerRead` in `hooks/use-document-viewer-read.ts` is a separate
read-only adapter. Supply the current workspace, source chat, current human,
verified `ViewerTarget`, `enabled: true` and a nonempty `activationKey`. The key
is a caller-generated auth/open epoch, never a credential. Change it on reopening
or any authority change, and supply current session identity synchronously.
The hook does not establish that a Page belongs to the source conversation;
the caller must obtain that relationship from verified server result links.

It reads current Page metadata and then saved Document content by encoded
immutable `@PageID`, through `apiFetch` with the current workspace and no-store.
Only exact Page identity, `has_document: true`, a nonempty title of at most 512
characters, supported schema 2/3, a safe revision and closed validated Document
content can produce a ready envelope. Creation result titles and revision 0 do
not pin a historical body. Only title and validated body metadata are retained;
engine snapshots, leases, generation and writer authority are not published.

Every identity/activation/target/enable or refresh change masks old data during
render. Prior requests are aborted and late results ignored. Page updates and
reconnects trigger a fresh generation. Denial hides all protected data; 503,
validation failures and other errors also discard old titles/bodies. The hook
has no shared query cache, writes, editor, Yjs document or layout/store mutation.
Caller wiring, browser acceptance and H1 remain incomplete until integrated.
