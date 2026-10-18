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
still-connected opener. It does not trap focus or declare a modal: the host
owns desktop split/mobile placement. Escape already consumed by a nested
control does not close the viewer. A parent closing the shell independently
must handle its own focus restoration.

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
