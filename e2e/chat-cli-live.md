# Live Chat CLI acceptance (Dev2)

This complements the isolated [browser suite](workspace-conversations.md).
It uses the real Go binary against the running Dev2 service, including real
member logins, SQLite writes and the background inbox projection. It does not
mock HTTP, mint session tokens directly or mutate SQLite from the test script.

Build the CLI from the current tree and use the correct authenticated profile:

```sh
go build -ldflags "$(scripts/build-stamp.sh ldflags)" -o /tmp/crewship-chat-cli-check ./cmd/crewship
/tmp/crewship-chat-cli-check --profile dev2 --server http://localhost:8082 whoami
python3 e2e/chat-cli-live.py \
  --binary /tmp/crewship-chat-cli-check \
  --profile dev2 --server http://localhost:8082 \
  --report /tmp/chat-cli-live-dev2.json
```

The test requires an owner profile. It creates a uniquely named `chat-cli-qa-*`
workspace and two new `example.invalid` accounts using `workspace member invite`.
No invitation email is sent. The account setup link is redeemed over the real
HTTP endpoint, then each account signs in using `login --password-stdin` into a
separate private CLI configuration. Existing profiles/passwords stay untouched.

Coverage: reverse-pair direct reuse; third-user read/write denial; fixed direct
roster; Unicode; stable send retry and conflict; one recipient inbox item with
canonical Chat URL; read cursor; mute/unmute; two-way history; both pagination
directions; group grant/revoke; workspace channel access; filtered room pages;
12 concurrent sends from two users with unique sequences and no lost messages.

On success, the script revokes the two test login sessions, deletes its exact
workspace with slug confirmation, and removes private configuration files.
The synthetic account identities may remain in the instance; their workspace
membership is removed, their sessions are revoked and random passwords discarded.
On failure it retains a mode-0700 state directory, records its location in the
report and exits nonzero. Inspect and clean up only the IDs in that report; do
not rerun setup repeatedly without cleaning failed runs. Never share config
files or setup links. The script refuses targets other than Dev2.

This script does not invoke models. Separate live agent-session and mixed-channel
checks are recorded in the [Dev2 report](../docs/prd/reports/chat-cli-agent-dev2-2026-09-07.md).

## CLI surface

Prefix these examples with the intended profile/server/workspace flags.
`chat room` has alias `chat rooms`. IDs come from JSON output or room/member lists.

```sh
crewship chat room list --limit 50 --offset 0 -f json
crewship chat room direct USER_ID -f json
crewship chat room create --kind group --title "Project discussion" --member USER_ID
crewship chat room create --kind channel --title "Team channel"
crewship chat room get ROOM_ID -f json
crewship chat room messages ROOM_ID --limit 50 --before-sequence 100 -f json
crewship chat room messages ROOM_ID --after-sequence 100 -f json
crewship chat room send ROOM_ID --message "Hello" --client-id UNIQUE_SEND_ID
crewship chat room read ROOM_ID --sequence 101
crewship chat room mute ROOM_ID
crewship chat room mute ROOM_ID --muted=false
crewship chat room participants list ROOM_ID
crewship chat room participants add ROOM_ID USER_ID
crewship chat room participants remove ROOM_ID USER_ID
crewship chat room agents add CHANNEL_ID AGENT_ID
crewship chat room agents list CHANNEL_ID
crewship chat room send CHANNEL_ID --message "Please review this topic" \
  --client-id UNIQUE_SEND_ID --mention-agent AGENT_ID
crewship chat room jobs CHANNEL_ID -f json
crewship chat room agents remove CHANNEL_ID AGENT_ID
```

Reuse the same `--client-id`, content and mentions for retries; changing content
under an existing key returns conflict. Repeat `--mention-agent` to address more
than one joined agent. Plain `@name` text does not execute an agent. Private human
groups and direct messages cannot admit agents; mixed rooms are workspace-visible
channels. `read` takes the last sequence actually seen, never an implicit latest.

Agent sessions retain `chat create`, `chat list`, `chat <id>`, `run --chat`,
`chat attach`, `chat attachments`, `chat rename`, `chat read`, and `chat delete`.
Room IDs and legacy agent-session IDs are distinct namespaces. All room commands
support the standard formatter; JSON preserves pagination envelopes. Workspace
create/member invite and agent attachment upload also produce valid machine
formats now. Invitation JSON contains a one-time setup secret: protect it.
