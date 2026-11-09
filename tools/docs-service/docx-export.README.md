# Native Document DOCX formatter foundation

`createDocumentDOCXExporter(validateJSON)` requires the trusted compiled engine's
synchronous validator. The returned `(content, schema)` function accepts the
engine's confirmed public ProseMirror `content`, not `documents.State.Projection`
(which is block-hash metadata). It returns `{bytes, report}` only after complete
validation and conversion. Missing/async/normalizing validators fail; the mapping
also rejects unknown nodes, attributes, marks, invalid text and structure itself.
There is no default validator or generic HTML/Markdown fallback.

The formatter accepts content schemas 1–4 and maps native paragraphs, headings 1–3, bullet/decimal lists with ordered
starts and at most nine levels, rectangular simple tables, schema3 hyperlinks,
and inline/block code styles. Links become external relationships; no URL is
fetched. Tabs and line breaks are native Word elements; CR/CRLF use LF line-break
semantics. XML-invalid controls and lone surrogates fail without replacement.
Nested simple tables end with the Word-required empty cell paragraph. Block IDs,
comments, CRDT identities/history and collaborative proposals are not a DOCX
roundtrip/import format. Schema 4 quotes, checklists
(☐/☑-led paragraphs), bold, italic and strikethrough are mapped; code block
languages are not represented in Word. Later schemas and images are explicitly
refused (`unsupported-schema`); no raster fallback or private asset retrieval
exists.

`DocxExportError` has `code` and `path` and contains no partial bytes. Unknown
schemas use `unsupported-schema`; invalid engine content uses `invalid-content`;
unrepresentable XML, ragged tables and excess Word list depth use
`unmappable-content`. The formatter has the existing 500,000-character JSON,
10,000-node and 40-level input ceilings and an 8 MiB compressed output ceiling.
These are conversion safety ceilings, not capacity or fidelity promises.
Identical content produces deterministic ZIP bytes with fixed timestamps.

This module performs no authorization, persistence, network or file writes.
Its one caller is the docs-service `export` operation with `format: "docx"`
(`GET /api/v1/pages/{slug}/document/export?format=docx`, `crewship doc export
--as docx`). That operation converts the content it has just validated from the
confirmed state the Go authority returned for the caller's own credential, so
live read access, private-output custody and its audit are rechecked on every
download; there is no cached or hash-addressed copy. The service caps the
package at 3 MiB so its base64 reply fits the Go client's 4 MiB read, and maps
`DocxExportError` to `422` with its code. `fflate` is pinned in the
docs-service package and lockfile, and the image copies this module.

The report's `qualification: 'unqualified'` is deliberate. ZIP/XML checks are
structural conversion evidence only. Current Microsoft 365 Word compatibility
requires an authorized exact-build/platform open/edit/save/reopen qualification;
LibreOffice automation alone cannot establish Microsoft 365 compatibility.
Fonts, layout, glyph coverage and Office substitutions are not qualified here.
No host Office packages are installed and no private Office session is used.

Focused tests use the real current compiled validator:

```sh
crewship-heavy node --test tools/docs-service/docx-export-node-test.mjs
```

They need the frozen `tools/docs-service` dependencies and run with the
service suite (`pnpm --dir tools/docs-service test`). Captured exports
and qualification reports belong outside the tracked source tree.
