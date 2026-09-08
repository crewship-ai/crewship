# Chat CLI parity and live Dev2 acceptance — 2026-09-07

Status: deployed to Dev2; live acceptance and complete Go regression passed.

The previous phase used an isolated authenticated browser fixture. This phase
adds the missing workspace-room CLI and proves the real Dev2 service accepts
multi-user operations and completes actual agent work. It uses existing profile
`dev2`, explicitly targeting instance 2. No user's password was reset and no
live session token was forged. The default active profile was `dev1`; explicitly
selecting `--profile dev2` was necessary to use the correct existing credentials.

## Delivered CLI

`crewship chat room` (`rooms` alias): list, create, direct, get, messages,
send, read, mute, jobs; participants and agents each support list/add/remove.
All 15 HTTP operations are covered. Standard workspace/auth scope is reused.
Sends require a stable client ID; retries preserve both message and job identity.
Mentions are explicit joined agent IDs; plain @text is inert. Pagination metadata
is preserved in JSON. Read requires the sequence actually seen.

Workspace create/member invite and chat attach previously ignored machine output
formats. These now honor JSON/YAML/NDJSON; quiet emits an ID/path/email rather
than prose or a setup token. Attachment decode failures now return errors.

Command examples and reproducible opt-in test:
[Chat CLI acceptance](../../../e2e/chat-cli-live.md).

## Actual live human chat checks

The final `e2e/chat-cli-live.py` run passed 13 scenario groups against
`http://localhost:8082`; [machine-readable report](chat-cli-dev2-2026-09-07.json),
run ID `1eebcc9b9e35`. It used the existing owner and two independently logged-in
synthetic users in an isolated workspace. Every chat operation used the compiled
CLI; only redemption of new-account setup links used the browser HTTP endpoint.

- Reverse-pair direct reuse, private read/write denial to a third workspace user,
  and immutable two-person membership.
- Unicode text, same-ID retry deduplication, changed-content conflict and real
  recipient history.
- Background outbox to inbox, canonical Chat link, no third-user notification,
  read clearing, mute without losing history, and unmute resuming notification.
- Two-way history, forward/backward pagination and filtered room pagination.
- Group add/read/send/remove and immediate access denial after removal.
- Workspace-visible channel access for an ordinary member.
- Twelve concurrent sends from two independently authenticated users: all 12
  unique message IDs/sequences and all 16 total expected messages persisted.

Both the initial and final QA workspaces were deleted by their exact slug/ID.
The initial run passed every chat assertion but its cleanup lacked the CLI's
`--yes` flag for session revocation; that test-harness defect was corrected,
the first run cleaned, and a fresh final run passed including cleanup. Test
sessions were revoked and private config directories removed. Four synthetic
`example.invalid` account identities may remain with no test-workspace access;
their random passwords were discarded. No real user's chat was used for these
human test messages.

## Actual live agent checks

[Detailed evidence](chat-cli-agent-dev2-2026-09-07.md): two distinct sessions on
Mařena, empty history, rename/read, synthetic attachment upload and
deletion, actual model output `CREWSHIP_CHAT_CLI_OK`, persisted user/assistant
history, terminal stream behavior, and exact test-resource cleanup. The user's
linked session remained present and its contents were not inspected.
After deployment, `agent files --download` also fetched the actual synthetic
UTF-8 attachment bytes; content and SHA-256 matched the original and metadata.

One retained QA channel verified agent add/list, a structured mention, durable
assignment, actual reply `CREWSHIP_CHANNEL_CLI_OK` in about 7.9 seconds, stable
retry with one job/reply, inert plain @text, and removal/list. Its final agent
roster is empty. It remains because room deletion is not part of the current API:

[Review the actual QA channel](https://crewship-dev2.unifylab.cz/chat?conversation=cmtr4cgmp32f59364c787a91e31f8d610&workspace_id=cmtplws8h000276fa560f).

CLI whoami, room get and jobs also succeeded through the public HTTPS Dev2 URL,
using an explicit server-mismatch override for the known public alias of the
same instance. The live job state was `completed` through that route too.

## Automated checks and limits

Focused CLI tests passed: 20 successful endpoint contracts, 16 invalid-input
cases making no HTTP request, six server failures, stable retry identity,
escaped IDs, all five formats and coexistence with legacy Chat commands.
Provisioning added 16 format/scenario cases; attachment output added six cases.
`go vet ./...` passed, as did targeted CLI regression under the race detector.
The `clionly` build passed; its generated command manifest exposed all 15 room
operations and that binary read the completed job from actual Dev2 successfully.
Full `go test ./... -count=1 -timeout=30m` passed, exit zero, including API,
database, backup and the complete CLI package. Final `go vet ./...` passed after
the help text update. Logs: `/tmp/chat-cli-go-all.log`,
`/tmp/chat-cli-vet-final.log`, `/tmp/chat-cli-race.log`. Diff check is clean.

These live checks establish functionality and access boundaries, not a 100-user
production SLA. The earlier isolated 100-client capacity test and 27-scenario
browser acceptance remain separate evidence. This phase changes CLI and tests;
it does not change the database schema or frontend rendering.

## Dev2 deployment and restart persistence

`sudo systemctl reload crewship-ws@2` completed successfully. The standard
`/tmp/crewship-2-dev` command manifest now includes `chat room`. Go on 8082 and
Next.js on 3012 are healthy; public HTTPS health returned `status: ok`.
After the reload, that standard binary verified exactly one persisted real
channel reply, one completed job and an empty final agent roster. This also
checks that the accepted channel state survives the actual service restart.
Schema remains `20260907085939`, 278 applied migrations and none outstanding.
The working branch remains uncommitted; deployment is not a merged PR.
