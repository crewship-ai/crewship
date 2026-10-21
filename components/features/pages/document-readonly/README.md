# Read-only Document body

`DocumentReadonly` accepts `schema: 2 | 3`, `content: unknown` and a caller-supplied
`accessibleLabel`. It validates the entire closed Document JSON before rendering
semantic DOM through ProseMirror's `DOMSerializer`. It never creates an editor,
Yjs document, transaction, lease or network request, and never parses HTML strings.
The shared schema module supplies the schema definitions; importing it does not
instantiate a Yjs document in this reader.

Malformed, non-document-root, unsupported-schema and unsafe-link content produces
a generic unavailable status. A changed invalid input removes the previous body.
Unknown content is never normalized into text that could later be saved. Safe
links follow the existing closed schema policy: canonical absolute HTTP(S) URLs,
without credentials, opening with `noopener noreferrer`.

This is a rendering boundary, not an authorization boundary. Callers must supply
only an authorized current read and remove the component when that authority is
invalidated. The label must not infer private titles from inaccessible metadata.
Keep the live editor or composer mounted separately to preserve human drafts.
History and chat integration, authorized network resolution and live acceptance
remain separate work; this component does not establish H1 acceptance.

Implementation provenance: the `ConfirmedContent` reader introduced in commit
`a621916a0e16a6228d539c5d7b0ea8e699605155`, reusing the existing
`lib/documents/schema.ts` closed schema. The original history and viewer files
are unchanged by this extraction.

Focused verification:

```sh
crewship-heavy pnpm exec vitest run components/features/pages/document-readonly/document-readonly.test.tsx
```
