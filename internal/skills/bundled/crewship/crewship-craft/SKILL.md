---
name: crewship-craft
display_name: Crewship Craft
version: 1.0.0
category: WRITING
license: Apache-2.0
runtime: INSTRUCTIONS
description: Edit a native Craft Word or Sheet document during a whole-document task with the craft_* tools, holding the edit lease, saving with exact versions and reconciling uncertain saves. Use when a person started a native Craft task for you; not for Project, Dashboard, Slides or Design artifacts.
---

## When to Activate

Use this playbook when your run is a native Craft task: a person asked you in
their chat to change one Word or Sheet document, then confirmed a
whole-document task for it. In that run the `crewship-craft` tool server offers
`craft_read`, `craft_acquire`, `craft_renew`, `craft_apply` and `craft_release`.

Use another playbook for other kinds of work:

- Project, Dashboard or Slides artifacts: `crewship-artifacts`.
- Design canvases: `crewship-design`.

## Rights and mandate

The tools work only inside the task that a person confirmed. They never let
you pick a document, person, workspace or server. The task covers the one
document and the instruction in that person's chat message, for at most one
hour, and the person can stop it at any time.

A person starts the task in the app or with
`crewship craft task start <page> --chat-id <chat> --message-id <message> --whole-document=true`.
The instance must enable native documents and native agents with
`CREWSHIP_CRAFT_NATIVE_ENABLED=true` and
`CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED=true`. Both are off by default.

If a tool answers `403` ("live native Craft task required" or "confirmed
native Craft task required") or `503` ("native Craft agents unavailable" or
"native documents unavailable"), stop. Tell the person that the task is not
active or native agents are switched off. Do not look for another way to
write the document.

## Tools

| Tool | Arguments | Result |
|---|---|---|
| `craft_read` | `query` (optional) | `application`, `version`, `engine_pin` and, with a query, `view` |
| `craft_acquire` | none | `generation`, `expires_at`, `holder_kind` |
| `craft_renew` | `generation` (required) | new `expires_at` for the same `generation` |
| `craft_apply` | `generation`, `base_version`, `request_id`, `commands` (all required) | save receipt with the new `version` |
| `craft_release` | `generation` (required) | `{"released": true}` |

`generation`, `base_version` and `version` are decimal strings such as
`"3"`, not JSON numbers. `application` is `wordcraft` for Word and
`gridcraft` for Sheet.

Read queries:

- Word: `{"command":"text","params":{}}` returns the document text.
- Sheet: `{"command":"cell.get","params":{"cell":"B2"}}` returns one cell.

Apply commands (1 to 64 per call and at most 64 KiB of JSON together; at
least one of them must change the document):

- Word: `{"command":"caret.docStart","params":{}}` or
  `{"command":"caret.docEnd","params":{}}` moves the caret;
  `{"command":"text.insert","params":{"text":"..."}}` inserts up to 64 KiB of
  text at the caret, within the 64 KiB call limit.
- Sheet: `{"command":"cell.set","params":{"cell":"B2","input":"42"}}` sets
  one cell. `input` is always a JSON string with a literal value. Formulas are
  refused: no leading `=`, and a leading `+` or `-` only for a number.
  Cells use upper-case A1 references from `A1` to `XFD1048576`.

The tool schema lists every command name for both tools. Use read commands
only in `craft_read` and write commands only in `craft_apply`; anything else
is refused with `422`.

`request_id` is your own ID for one edit: 1 to 128 characters from
`A-Z a-z 0-9 _ . : -`. Use a new one for each new edit, and keep the exact
arguments of every apply until you have its receipt.

## Procedure

1. `craft_read` with a query to see the current content and `version`.
   Reading never takes the lease.
2. `craft_acquire`. Keep the returned `generation`. The lease runs out about
   15 seconds after it was last renewed, so call `craft_renew` if you need more
   time before you apply. Each `craft_apply` renews it as well.
3. `craft_apply` with that `generation`, the `version` you read as
   `base_version`, a new `request_id` and only the commands the person asked
   for.
4. Check the receipt. Only a successful `craft_apply` response with its
   `version` proves that the change was saved. Never say "saved" without it.
5. `craft_release` with the same `generation`.
6. Tell the person in chat what you changed and the saved `version`.

## When someone else holds the lease

`craft_acquire` returns `409` "document already has an editor" with
`holder_kind` `human`, `agent` or `unknown`. Do not write and do not retry in
a loop. The holder must be able to keep working undisturbed. Reply to the
person who gave you the task: say that the document is being edited (by a
person or by another agent), what you would change, and ask them to start
the task again when the document is free.

## Errors

- `409` "native document or lease changed; keep the local draft": your lease
  is no longer current, `base_version` is no longer the latest version, or
  you reused a `request_id` with different arguments. Nothing was saved.
  Run `craft_read` again and plan the edit against the new content. If your
  lease was lost, call `craft_acquire` once; if it is busy, follow the section
  above. Never just copy the newer version into `base_version` and resend.
- `413` "native history quota reached; existing versions retained": the
  document's history is full. A task also allows at most 64 different
  `request_id` values. Stop writing. Keep your commands and tell the person
  that nothing more can be saved until they free space.
- `422`: a query or command is not allowed or the document engine refused it.
  Fix the command. Do not resend it unchanged.
- `503` "native task outcome uncertain; retain exact original request" (the
  response echoes your `request`) or "original native save outcome
  unconfirmed; reconcile the exact original request": the save may or may not
  have happened. Send the exact same `craft_apply` arguments again, with the
  same `request_id`, `generation`, `base_version` and `commands`. The host then
  checks that one saved attempt instead of running the commands twice. Do not
  start any other edit first, because new edits are refused with `409` until
  the uncertain one is settled. If this check fails with `409`, for example
  because the lease ran out, stop and tell the person that the save outcome is
  unknown and that they should check the document.

## Comments and replies

You have no tool for document comments. Answer the person in the chat where
the task came from, and give the saved `version` from the receipt.
