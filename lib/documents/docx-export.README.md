# Native Document DOCX formatter foundation

`createDocumentDOCXExporter(validateJSON)` requires the trusted compiled engine's
synchronous validator. The returned `(content, schema)` function accepts the
engine's confirmed public ProseMirror `content`, not `documents.State.Projection`
(which is block-hash metadata). It returns `{bytes, report}` only after complete
validation and conversion. Missing/async/normalizing validators fail; the mapping
also rejects unknown nodes, attributes, marks, invalid text and structure itself.
There is no default validator or generic HTML/Markdown fallback.

The current compiled validator explicitly accepts content schema 1/2/3. The
formatter maps native paragraphs, headings 1–3, bullet/decimal lists with ordered
starts and at most nine levels, rectangular simple tables, schema3 hyperlinks,
and inline/block code styles. Links become external relationships; no URL is
fetched. Tabs and line breaks are native Word elements; CR/CRLF use LF line-break
semantics. XML-invalid controls and lone surrogates fail without replacement.
Nested simple tables end with the Word-required empty cell paragraph. Block IDs,
comments, CRDT identities/history and collaborative proposals are not a DOCX
roundtrip/import format. Images/schema4 and other unimplemented rich marks are
explicitly unavailable; no raster fallback or private asset retrieval exists.

`DocxExportError` has `code` and `path` and contains no partial bytes. Unknown
schemas use `unsupported-schema`; invalid engine content uses `invalid-content`;
unrepresentable XML, ragged tables and excess Word list depth use
`unmappable-content`. The formatter has the existing 500,000-character JSON,
10,000-node and 40-level input ceilings and an 8 MiB compressed output ceiling.
These are conversion safety ceilings, not capacity or fidelity promises.
Identical content produces deterministic ZIP bytes with fixed timestamps.

This module performs no authorization, persistence, network, file writes, API,
CLI or runtime registration. A future caller must pin the saved identity/revision,
obtain actual validated engine content, bind its report to source/output hashes,
and recheck live READ/private-asset access before delivery. A hash is no grant.
The isolated docs-service image does not install this root-package dependency;
this cut makes no service packaging change. It reuses locked root `fflate`.

The report's `qualification: 'unqualified'` is deliberate. ZIP/XML checks are
structural conversion evidence only. Current Microsoft 365 Word compatibility
requires an authorized exact-build/platform open/edit/save/reopen qualification;
LibreOffice automation alone cannot establish Microsoft 365 compatibility.
Fonts, layout, glyph coverage and Office substitutions are not qualified here.
No host Office packages are installed and no private Office session is used.

Focused tests use the real current compiled validator:

```sh
crewship-heavy node --test lib/documents/docx-export-node-test.mjs
```

They need the existing pinned root dependencies and the separately managed
`tools/docs-service` dependencies; the module installs neither. Captured exports
and qualification reports belong outside the tracked source tree.
