# Live Dev2 CLI verification: agent sessions

Executed 2026-09-07 10:44–10:46 UTC against the actual instance-2 service, not
an isolated fixture. All commands explicitly used
`/tmp/crewship-2-dev --profile dev2 --server http://localhost:8082`.
Credentials came from the existing Dev2 CLI profile; no credentials were
printed or created. Application source was not modified by this test.

## Results

| Scenario | Live result |
| --- | --- |
| Resolve Mařena | `agent list --q ma-ena --format json` resolved `cmtpmjabx002425d82ae7`, slug `ma-ena`, idle. |
| Preserve the user's linked session | Session `b040147e-6f55-402e-9c9e-a52ae65ebfdf` existed in the initial and final list. Its transcript was not read and it was not modified. |
| Create two sessions for the same agent | `chat create ma-ena --format json` returned two distinct IDs; both appeared under `chat list ma-ena --kind direct`. |
| Rename both sessions | Both persisted unique titles prefixed `CLI Dev2 smoke 2026-09-07 session`, suffixed A/B. |
| Empty session history | Both `chat <id> --format json` calls exited zero with empty history before sending. Both list rows reported `message_count: 0`. |
| Read cursor | `chat read <id> --format json` passed for both empty sessions and again after the real response. Initial unread counts were zero. |
| Actual model response | One bounded real run returned exactly `CREWSHIP_CHAT_CLI_OK`, then `[done]`, exit zero. No retries or model switching were used. |
| Persisted real history | Fetching only the newly created test session returned exactly two messages, roles user and assistant; the assistant marker was present. |
| Terminal streaming state | `chat stream <id> --idle 5 --format ndjson` after completion returned `active:false` and `stream.end` reason `no_active_run`, exit zero. This does not establish replay of completed streams. |
| File upload | Uploaded a synthetic 57-byte text file to test session B. |
| Attachment list/integrity | The attachment list reported the same byte count and SHA-256 as the local synthetic file. This verifies reported integrity metadata, not a separate download of the bytes. |
| Attachment delete/idempotence | Deleted the exact uploaded attachment, verified an empty list, and repeated deletion successfully. |
| Cleanup | Deleted only the two exact newly created test sessions after the run was terminal. Final list returned to its initial 15 rows; both test IDs were absent and the user's linked ID remained. |

The exact real invocation was:

```sh
/tmp/crewship-2-dev --profile dev2 --server http://localhost:8082 \
  run ma-ena --chat cmtr4604c00101e9b1b43 \
  --timeout 90 --max-turns 1 --no-markdown \
  'Reply with exactly CREWSHIP_CHAT_CLI_OK. Do not use tools or change files.'
```

Test resource IDs, retained only as an audit trail (already deleted):

- Session A: `cmtr4604c00101e9b1b43`.
- Session B: `cmtr467tk0011334fb841`.
- Attachment: `cmtr46r8j00120e823479`.

## CLI inconsistency found

`chat attach ... --format json` succeeded but emitted a human-readable
`Uploaded ...` line instead of JSON. `chat create`, `rename`, `read`,
`attachments list`, `attachments delete`, and `delete` all returned usable
JSON. This is an automation/output-format defect, not an upload failure;
reported to the parent implementation task for correction and regression
coverage.

## Scope

This live run establishes agent session creation, read/write history, the
configured real model path, and attachment lifecycle on Dev2. It does not
claim multi-user isolation from a single authenticated profile, group-agent
dispatch, sustained load, or browser rendering; those require their own
scenarios. The minimal real invocation may leave its normal run/audit/billing
record even though the disposable chat and attachment were cleaned up.

## Follow-up: corrected CLI and real channel agent dispatch

Executed 10:49–10:51 UTC using the freshly built
`/tmp/crewship-chat-cli-check`, with the same explicit Dev2 profile/server.

The attachment JSON correction passed live: a new disposable Mařena session
accepted the 57-byte file and `chat attach --format json` returned parseable
JSON with `filename`, `size: 57`, `path`, and `agent_path`. The attachment was
verified through `attachments list`, then deleted together with its disposable
session. The original output-format defect above is resolved in this build.

For the channel scenario, `workspace member list` confirmed that the demo
workspace `cmtplws8h000276fa560f` had exactly one member, its existing owner.
No new user, credential, workspace setting, or existing conversation was
modified. Every channel command explicitly selected that workspace.

| Scenario | Actual Dev2 result |
| --- | --- |
| Create workspace channel | `CLI QA agent channel 2026-09-07`, canonical ID `cmtr4cgmp32f59364c787a91e31f8d610`. |
| Add/list agent | `chat room agents add` added Mařena; `agents list` showed her ID and name. A visible join notice disclosed history access. |
| Structured mention | `chat room send` with `--mention-agent cmtpmjabx002425d82ae7` and stable client ID `cli-qa-agent-channel-20260907-1049` created one pending job. |
| Real queue/model/reply path | Job `cmtr4cs0k1d0ea807fb73fa8ae034737d` acquired assignment `cmtr4csrv0020f3f1290a` and reached `completed` with empty error. Mařena's actual reply was exactly `CREWSHIP_CHANNEL_CLI_OK`. |
| Response timing | Human message stored at 10:49:59.251Z, agent reply at 10:50:07.174Z: approximately 7.9 seconds. |
| Stable send retry | Repeating the same message, mention and client ID returned the original message ID/sequence. Afterwards there was still exactly one job and one agent reply. |
| Plain @text | Sending `Plain @ma-ena text only; this message must not invoke an agent.` without structured mention IDs created a human message, no additional job or agent reply. |
| Remove/list agent | `chat room agents remove` succeeded; final agent roster was empty. |

The invocation prompt was `Reply with exactly CREWSHIP_CHANNEL_CLI_OK. Do not
use tools or change files.` No failed-run retry or model switch was needed.
Only synthetic QA messages were inspected. This establishes the real human
message → structured mention → assignment queue → configured model → shared
channel reply path, in addition to the earlier direct agent session test.

The clearly labeled QA channel remains for review because this API has no
channel-delete operation. Its agent roster is empty after the lifecycle test:

[Open the retained QA channel on Dev2](https://crewship-dev2.unifylab.cz/chat?conversation=cmtr4cgmp32f59364c787a91e31f8d610&workspace_id=cmtplws8h000276fa560f).

## Final deployed CLI: actual Files download parity

After the standard `/tmp/crewship-2-dev` binary was rebuilt and Dev2 reloaded,
the same explicit Dev2 profile/server passed an actual attachment download
through `agent files ma-ena --download <returned-relative-path> --out <file>`.
This exercises the file source also used by the web Files panel.

- Created one disposable session `cmtr4qhbk0003e67f503f` without invoking a model.
- Attached a 51-byte synthetic UTF-8 text file, including the name Mařena;
  standard deployed `chat attach --format json` returned usable JSON.
- Downloaded the actual bytes via `agent files`, asserted exact byte equality
  with the original, and matched SHA-256 against attachment metadata:
  `5abd259f2f4246d4f2fd9c859a153e823270bddab9ed49d5eef0449b22a204a1`.
- Deleted only this attachment/session and the local temporary directory.
- Confirmed the disposable session was absent and the user's linked session
  remained present. Final Mařena list contained 16 sessions; this check did
  not delete other sessions (including the channel assignment's session).

This closes the earlier limitation where attachment integrity was checked
only against metadata: the real stored file was fetched and compared too.
