---
name: crewship-artifacts
description: Create or revise a private HTML artifact in Crewship Pages. Use for publishing an agent output into Crewship, not for sharing files publicly or changing Page permissions.
---

# Crewship artifacts

Produce the requested output, publish through the current run's sidecar authority,
and return the server-confirmed Page link and revision. A local file alone is not
a published artifact. This skill does not grant access or start a new agent run.

## Prepare the output

Read the requested source revision before editing. Use the
run's **SIDECAR AUTH** instructions from the system prompt; keep credentials out
of command arguments, generated HTML, screenshots and replies. Do not substitute
a crew token or another run's identity when the server refuses access.

The current artifact preview is **static**. Build a self-contained UTF-8 HTML
document, with inline CSS and local data images, at most 10 MiB. Scripts, external
resources, forms and navigation do not execute in the preview. Use semantic HTML,
readable text and responsive layout. For charts, render a static representation.
Do not promise a working React application or interactive controls while this
runtime profile is disabled. If interactivity is essential, preserve the source
and explain the unavailable runtime instead of silently presenting a static mock
as a working application.

Use existing project dependencies and build tooling when source is provided.
Do not install tools on the Crewship server or start another hosting service.
If generating a bundle, inline its assets and inspect the result for network
dependencies. Publishing the HTML build does not retain original React/TypeScript
sources automatically.

## Publish or revise

- Prefer the named tools advertised by `crewship-routines`: `artifact_create`,
  `artifact_publish`, `artifact_read`, `artifact_comments`, `artifact_reply`,
  `artifact_request_read`, `artifact_request_update`, `artifact_list`, `artifact_diff` and `artifact_restore`. They call the same authorized sidecar handlers
  described below. The MCP request is limited to 1 MiB; large HTML uses the
  sidecar HTTP flow. An oversized result is not proof a mutation failed:
  retain its original mutation ID and check through HTTP.
- The workspace administrator must enable artifact publication for this crew.
  This is additional to human run authorization and Page grants. Neither a
  comment nor an MCP tool call can change that policy. A disabled policy is
  not a reason to try another crew or identity.
- New output: a live human chat run needs explicit creation authorization.
  `POST /artifacts` accepts `slug`, `name`, `html` and a stable `mutation_id`.
  The server chooses the human owner and private audience. This authorization
  permits one new artifact, not an arbitrary set of Pages.
- Existing output: read `GET /artifacts/<slug>?revision=<number>` and publish
  `POST /artifacts/<slug>/revisions` with `html`, observed `base_rev`, stable
  `mutation_id` and the supplied `request_id` for an assigned change request.
  An explicit agent Page write grant and current run authority are required.
  For an assigned request, first pull its immutable discussion through
  `GET /artifact-requests/<request_id>` after run admission. Later comments do
  not silently replace that snapshot, and revoked access stops further reads.
- Encode JSON from the local file with a JSON library. Send the request file
  with `--data-binary`; do not interpolate HTML into shell commands.
- After a lost response, retry the **same body and mutation ID**. On 409, retain
  the output and report the conflict; a fresh ID or newer base revision is not
  permission to overwrite someone else's work. On 403, stop publication and
  explain the missing authority. On 429, respect the server's retry guidance;
  do not rotate through artifacts to evade a workspace limit.
- Report success only after a successful server response. Return its revision
  and Page URL, and distinguish private publication from public sharing.

Reads include a verified single-file manifest (`index.html`, byte size and
SHA-256) and `manifest_status`. `legacy_derived` describes older content whose
manifest was derived at read time; `recorded` describes a manifest stored with
the revision. The only supported profile is `static-html-v1`, format version 1.
Publication may omit these two fields for compatibility. Unsupported profiles
are rejected; a profile name cannot enable scripts or platform capabilities.


`artifact_list` contains only this run's explicit live delegations. Follow
`next_cursor` while `has_more`; a scan page may be empty after revoked entries
are removed. `artifact_diff` compares two exact retained revisions within
256 KiB each; a larger source returns `413` with no partial result. Read the
exact authorized HTML revisions and compare locally in that case.
`artifact_restore` creates a new agent-authored revision from a retained source:
keep the captured `base_rev`, source `revision`, bound `request_id` when present,
and `mutation_id` unchanged on uncertain retry. Never silently rebase or force.
## Reply to artifact comments

Read `GET /artifacts/<slug>/threads?limit=5` with the current run's sidecar
identity. The response is paginated: follow `next_cursor` as `cursor` when more
threads are needed. An explicit agent `comment` or `write` grant, a live verified
human run, explicit current Page/run delegation and that human's current
comment permission are all required. A chat message alone never delegates Page
work. Request-backed delegation also requires the initiator's current editor
authority, so a commenter cannot dispatch a stronger agent by opening a chat.

Reply to an open thread with
`POST /artifacts/<slug>/threads/<thread_id>/comments`, sending `body`, the
observed thread `base_version`, and a stable `mutation_id`. Replies are plain
text, at most 4 KiB; the server records the agent, run and human provenance.
Retry a lost response with the exact original body and ID. On a version conflict,
read the new replies before deliberately preparing a new submission. A comment
grant does not authorize publishing, resolving threads or assigning work.
