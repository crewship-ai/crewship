---
name: crewship-design
display_name: Crewship Design
version: 1.0.0
category: DESIGN
license: Apache-2.0
runtime: INSTRUCTIONS
description: Edit an existing Crewship Design artifact (a canvas of frames, rectangles, text and images) with the design_* tools, keeping node IDs stable and every change a retained version. Use when the owner sent you a node comment from the Design editor through a connected agent chat; not for Project, Dashboard or Slides artifacts or native Craft documents.
---

## When to Activate

Use this playbook when your run was started for one existing Design artifact
by a node comment from the Design editor. The owner first connects one of
their private chats with you to the Design ("Connect agent chat" in the
editor, or `crewship page design agent <slug> --chat-id <chat>`). After that,
a comment they send on a selected node ("Send to agent") reaches you in that
chat as a Design request. A plain chat message does not give you a Design
mandate. A Design is a canvas of stable nodes shared live with people who
have it open.

Use another playbook for other kinds of work:

- Project, Dashboard or Slides artifacts: `crewship-artifacts`.
- Native Word or Sheet documents: `crewship-craft`.

You cannot create a Design. Creation is a human action; if the owner wants a
new canvas, ask them to create it in the Artifacts library (or with
`crewship page design create <slug> --name <title>`), connect your chat to it
and send you the request as a comment. The Artifacts library is off by
default and needs the build-time `NEXT_PUBLIC_ARTIFACTS_LIBRARY=true`.

## Rights and mandate

The `design_get`, `design_apply`, `design_snapshot`, `design_versions` and
`design_restore` tools are always listed, but a listed tool grants nothing.
Every call succeeds only when all of these hold:

- Design is enabled for the workspace: the `artifacts_design` workspace flag
  (`crewship feature-flag enable artifacts_design`) or the instance switch
  `CREWSHIP_ARTIFACTS_DESIGN_ENABLED=true`. Both are off by default. The
  browser canvas also needs the build-time `NEXT_PUBLIC_ARTIFACTS_DESIGN=true`,
  and creating a Design in the browser needs the Artifacts library
  (`NEXT_PUBLIC_ARTIFACTS_LIBRARY=true`). Both build-time switches are off by
  default.
- Your crew is on the workspace's agent artifact policy
  (`crewship workspace artifact-policy show`).
- This run comes from a Design comment the owner sent from the editor to
  their connected private chat with you, and that request names this exact
  Design. The mandate covers that one Design for this run only.
- The owner still has edit rights to the Design.

You never choose a person, workspace, server or other Design in tool
arguments. If a call returns `403` or `404`, do not try another slug or
identity. Tell the owner which permission is missing and ask them to send the
comment again from the Design editor once it is in place.

## Tools

| Tool | Arguments | Result |
|---|---|---|
| `design_get` | `slug` (required); `version` (optional integer ≥ 1) | `nodes`, `order`, `can_edit`, `can_comment`, `version`, `latest_version`, `shared_version` |
| `design_apply` | `slug`, `operations` (both required); `message` (optional) | `nodes`, `order` and the new `version` |
| `design_snapshot` | `slug` (required); `message` (optional) | `version` receipt |
| `design_versions` | `slug` (required) | retained `versions`, `latest_version`, `shared_version` |
| `design_restore` | `slug`, `version` (both required) | `version` receipt of the new version |

`slug` is the Design's slug, or `@<page_id>` when the host gave you the
immutable Page ID. Leave out `version` in `design_get` to read the live canvas,
which is what people see right now. With `version`, you read that retained
snapshot instead, and both `version` and `latest_version` in the result show
the version you asked for. Use `design_versions` to learn the real latest
version.

## Procedure

1. Read the live canvas with `design_get`. Check that `can_edit` is `true`.
2. Find the nodes you will change by `id`. Keep every existing ID, including
   the IDs of nodes you only move or recolor.
3. Send all intended changes in one `design_apply` call with a short
   `message`.
4. Report the returned `version` to the owner only after the tool succeeds.

`operations` holds 1 to 500 entries, and each entry has exactly one payload:

```json
{"op":"upsert","node":{"id":"cart","type":"frame","name":"Cart","x":20,"y":20,"width":390,"height":844,"fill":"#ffffff"}}
{"op":"delete","id":"promo-banner"}
{"op":"reorder","order":["cart","cart-title","checkout-button"]}
```

- `upsert` creates a node or changes only the fields you send for an existing
  one. A new node needs `id`, `type` (`frame`, `rect`, `text` or `image`) and
  numeric `x`, `y`, `width` and `height`. Optional fields are `name`, `fill`,
  `parentId`, `text` and `src`. No other node field is accepted.
- `delete` removes the node with that `id` from the canvas and the order.
- `reorder` replaces the whole drawing order. Include every existing node ID
  once, in the new order. An ID that does not exist is refused.

Limits: IDs up to 128 characters, coordinates and sizes within ±1,000,000
(sizes not negative), `text` up to 100,000 characters, at most 2,000 nodes,
and the canvas must stay under 4 MiB. A `message` is at most 4,096 bytes. The
tool call itself is limited to 1 MiB of arguments, so a large image `src`
may not fit.

## Versions

Every successful `design_apply` and `design_restore` already records a new
retained version. Do not call `design_snapshot` after your own apply.
Use `design_snapshot` only when the owner asks you to keep the current live
canvas, for example after people edited it in the browser.

`design_restore` copies a retained version into a new version and pushes it to
everyone on the canvas. It never deletes history. Read `design_versions` first
and restore only the version the owner named.

The shared version is what readers see. Your applies, snapshots and restores
never change it. Only a person can share a version.

## Errors

- `409` "Design changed; reload and reapply": someone changed the canvas while
  your change was being saved. Nothing of yours was saved. Run `design_get`
  again, check that the nodes you meant to change still exist and still need
  the change, and then send one new apply. Never resend the same operations
  in a loop, and do not overwrite a person's newer edit without asking.
- `413` "Design capacity is full; existing work is retained": the Design's
  version history is full. Stop writing. Keep your intended operations, tell
  the owner the history is full and ask how they want to free space.
- `400` "invalid Design update": the operations broke a rule above, or the
  canvas would exceed its limits. Fix the operations; do not repeat them as
  they are.
- `403` or `404`: permission or mandate is missing. See Rights and mandate.
- `503` "outcome unavailable; inspect Design before retrying" or "Design
  engine is unavailable": the result is unknown or the engine did not run.
  Run `design_get` and `design_versions` first. If your change is already
  there, report it with that version; send it again only if it is absent.

## Comments

You have no tool for posting comments. When a Design comment started your
run, the comment names the node (its `id` in the selector) and the version
the person looked at. Check with `design_get` that this node still exists and
matches the quoted name or text. A successful `design_apply` in that run
marks the comment answered and adds the reply "Updated the selected Design
element." with the new version. If the node is gone or the request is unclear,
do not guess: ask the owner in chat which node they mean.

## CLI equivalents

Inside a run, `crewship page design get|apply|snapshot|versions|restore`
calls these same tools with your run's own credentials. `apply` takes
`--file changes.json` with `{"operations":[...],"message":"..."}`, `get` takes
`--version N`, and `restore` takes `--version N`. Do not pass `--server`,
`--profile`, `--workspace` or `--config` in a run. The human commands
`create`, `comment`, `comments`, `share`, `agent` and `watch` are refused in a
run.
