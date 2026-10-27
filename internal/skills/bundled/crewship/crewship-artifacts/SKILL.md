---
name: crewship-artifacts
metadata:
  display_name: Crewship Artifacts
  version: 1.0.0
  category: DEVELOPMENT
description: Create and revise a private multi-screen HTML project in Crewship with anchored owner comments and restorable versions. Use for a Crewship artifact project, not a native Craft document or a general website deployment.
---

Use the `artifact_create`, `artifact_versions`, `artifact_publish`,
`artifact_restore`, `artifact_comments` and `artifact_reply` tools from the
Crewship sidecar. The backend must enable artifact projects and the workspace
must allow this crew. The owner must explicitly authorize the exact live chat
run for either one new project or one existing project. A captured conversation
identity alone is not permission. If permission is missing, ask the owner to
use the chat's authorization action; never switch credentials or nominate an
owner, workspace, audience or human identity in tool arguments.

For a new project, supply `slug`, `name`, `entrypoint` and the complete `files`
array. Each file has `path`, UTF-8 `content` and `media_type`. Use relative paths
for linked screens and assets. A three-screen prototype can use `index.html`,
`details.html` and `settings.html` with ordinary relative links. Include every
file needed by the preview in one manifest. File paths must be relative,
non-traversing and unique. Keep selection targets stable with explicit element
IDs on every meaningful selectable element. Prefix them, for example
`artifact-hero-heading` or `artifact-primary-action`; the sanitizer removes
reserved IDs such as `title`, and positional selectors cannot authorize edits.
The first preview executes no authored JavaScript. Use three HTML files with
inline CSS or `text/css` files and ordinary local links, rather than relying on
a React bundle or runtime script. The trusted preview bridge handles navigation
and selection.

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
