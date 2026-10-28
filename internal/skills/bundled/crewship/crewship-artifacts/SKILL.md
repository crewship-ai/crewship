---
name: crewship-artifacts
display_name: Crewship Artifacts
version: 1.1.0
category: CODING
license: Apache-2.0
runtime: INSTRUCTIONS
description: Create and revise Crewship Project, Dashboard and Slides artifacts with complete manifests, data version checks, anchored agent feedback and restorable code versions. Use for these artifacts, not native Craft documents or website deployment.
---

## When to Activate

Use this playbook for a multi-screen Project, an inert data-bound Dashboard,
or a closed-profile Slides deck. The compiled packages are `artifact-project`,
`artifact-dashboard` and `artifact-slides`, release 1.1.0 / contract 1. Package
metadata describes shipped capabilities; it does not enable a backend or grant
access. Keep type instructions and host renderer sources separate from the
instance's files and datasets. Never attempt to replace a bundled skill or host
package through an artifact file upload. A package upgrade does not publish a
new instance version.

## Shared lifecycle and authority

Use the `artifact_create`, `artifact_versions`, `artifact_publish`,
`artifact_restore`, `artifact_comments` and `artifact_reply` tools from the
Crewship sidecar. The backend must enable artifact projects and the workspace
must allow this crew. The owner must explicitly authorize the exact live chat
run for either one new project or one existing project. A captured conversation
identity alone is not permission. If permission is missing, ask the owner to
use the chat's authorization action; never switch credentials or nominate an
owner, workspace, audience or human identity in tool arguments.

## Project

For a new Project, supply `slug`, `name`, `entrypoint` and the complete `files`
array. Each file has `path`, UTF-8 `content` and `media_type`. Use relative paths
for linked HTML screens and local CSS. A three-screen prototype can use `index.html`,
`details.html` and `settings.html` with ordinary relative links. Include every
HTML/CSS file needed by the preview in one manifest. File paths must be relative,
non-traversing and unique. Keep selection targets stable with explicit element
IDs on every meaningful selectable element. Prefix them, for example
`artifact-hero-heading` or `artifact-primary-action`; the sanitizer removes
reserved IDs such as `title`, and positional selectors cannot authorize edits.
This Project preview executes no authored JavaScript. Use three HTML files with
inline CSS or `text/css` files and ordinary local links, rather than relying on
a React bundle or runtime script. The trusted preview bridge handles navigation
and selection.

## Versioned edits and comments

When the host gives an immutable Page ID, use `@<page_id>` as the tools'
`slug` argument. An ordinary project slug also works.

Before changing a project, read `artifact_versions` and retain its exact version
and complete file set. Send the full proposed set to `artifact_publish` with
that version as `base_version`. Publication is atomic. Do not send only the
changed file: omitted files are absent from the new manifest.

Read `artifact_comments` before acting on a selected element. A comment addresses
an immutable `version`, file `path`, element `selector` and captured `quote`.
Read that version and verify the anchor identifies the intended element. If the
current version or anchor differs, ask for a fresh selection or explain the
comparison before editing; do not apply the old selection to another element.
After saving, call `artifact_reply` with the comment ID, the new version and a
concise account of the actual change. Ask for clarification in the thread when
the target is ambiguous: set `needs_clarification:true`, address the original
comment version and describe the fresh selection needed. This records no edit.

A 409 means another save won. Preserve your candidate, explain the conflict and
compare against the new version before proposing a reconciled save. Never
blindly replace `base_version` with the latest number. A full quota preserves
all previous versions; retain your unsaved candidate and report the storage
choice. An unavailable response is an uncertain outcome: inspect the project
before retrying creation or another write, because it may already have saved.

`artifact_restore` creates a new version from an older retained version. Pass
both the exact current `base_version` and the retained `version`. It does not
erase history. The shared version is separate from latest; publishing or
restoring must not silently change what readers see. Return the project URL and
confirmed version only after the tool acknowledges success.

CLI equivalents use `crewship page artifact create|publish|versions|get|restore|comments|reply`.
Use `--file manifest.json` for a complete manifest and `--base-version N` for
publish/restore. An agent run uses the scoped tools above rather than a human
CLI profile. The owner can pin a retained version with `page artifact share`.


## Dashboard

Require both `artifact_projects` and `artifact_dashboard_data` plus the exact
workspace/crew policy and live run mandate. Discover `artifact_data_batch`,
`artifact_data_read` and `artifact_data_collections` before relying on them.
The preview must opt in to Dashboard mode; never deliver dataset values to an
ordinary Project's authored JavaScript.

Publish `dashboard.json` with exactly
`{"schema_version":1,"collections":["sales"]}` alongside the HTML and local
CSS files in one code manifest. A Dashboard declares at most 32 collections.
Collection and row IDs match `[A-Za-z0-9][A-Za-z0-9_-]{0,127}`. Bind a value with
`data-source="sales" data-row="north" data-field="revenue"`. A flat field key is
not a JavaScript expression or path; use only an own key from the JSON object.
Use an inert `data-row="*"` repeat template for rows added later. Keep static
comment labels outside data-bound descendants; repeated clones lose authored
IDs. Native `<meter>` supports simple charts with static `min` and `max`.
Scripts, external requests, SVG/canvas and arbitrary SDKs are not supported by
this inert profile.

For data changes, read the current documents and send `artifact_data_batch`
with the exact code `base_version`, `collection`, and `documents` entries
`{"id":"north","ifVersion":N,"value":{"revenue":42}}`. Use `ifVersion:0` only
for an absent document. A batch checks all document versions atomically; retain
the candidate and re-read after a conflict rather than incrementing versions
blindly. The server owns actor/timestamp attribution. Each JSON object is at
most 256 KiB; a batch has at most 50 writes and 1 MiB; a collection has at most
4096 documents and 8 MiB of values. The preview has a separate aggregate 16 MiB
wire budget and 16 MiB expanded text budget, plus 100,000 estimated elements;
valid storage can exceed preview capacity and must show a refusal, not partial
data. Unsafe integer values are unavailable in the browser, never rounded.

Data-only writes do not publish a new code version, answer a design comment,
or move the shared code pin. A selected static design comment instead uses
the common publish/reply lifecycle. Readers see the current data only for
collections declared by their current shared code version, under their own
identity. Source labels and freshness are host-projected, never an invented
connector lineage. Restoring code does not restore an older dataset.

## Slides

Require `artifact_projects` and `artifact_slides`. Discover
`artifact_slides_read` and `artifact_export_pdf`; both require an explicit
positive selected code version. A reader's version must match the current
shared pin. Do not use a native Slides editor or write native slide storage.

Publish exactly `deck.json` plus one `slides/<id>.html` for each unique entry
in `{"schema_version":1,"order":["intro","details"]}`. No extra files,
network resources, scripts, CSS imports, custom fonts or unknown styles are
accepted. Do not attach a Dashboard index to a Slides manifest. Use 1920×1080
with integer, contained `left/top/width/height` in `px`; use literal `#RRGGBB`
colors and the fixed `Go Regular` font (`sans-serif` is a canonical alias).
The profile supports plain text `<p>`, rectangle/ellipse `<div data-shape="rectangle">` or
`<div data-shape="ellipse">`,
and `<table>` primitives. Start with this complete slide file:

```html
<section id="intro"><p id="artifact-intro-title" style="left:80px;top:80px;width:1600px;height:180px;font-size:48px;color:#112233;text-align:left">Morning report</p><aside id="artifact-intro-notes">Speaker notes</aside></section>
```

Every authored primitive (`p`, shape `div`, `table`), section and optional
`<aside>` requires an explicit quoted ID, unique across the deck. Keep table
structure (`tbody`, `tr`, `td`) plain, without extra attributes. Notes are at most 4000
Unicode code points. Use at most 100 slides, 200 elements per slide and 1000
across the deck; canonical typed JSON is bounded to 2 MiB. Tables are at most
30×20 with 4096 bytes per cell, 2000 cells per slide and 10,000 per deck.
Plain text is limited to 32 KiB per element. Leave
ample text space: PDF export rejects unsupported glyphs or text that does not
fit, rather than clipping or silently substituting a font. PDF output is
bounded to 8 MiB.

Read the server's typed projection and authored anchor map before editing a
selected slide element. Reuse its exact `slide_id`, `path`, `selector` and
canonical quote; do not invent selectors from generated SVG/canvas nodes.
Then publish the complete deck with the existing manifest CAS and reply to
the saved comment. Export only the explicit authorized code version; report
an unsupported or unavailable response honestly. Presentation, thumbnails and
PDF are host capabilities, not custom scripts inside the slide HTML.
