# Workspace conversation browser acceptance

This exercises the actual static frontend with an isolated migrated SQLite API,
real JWT sessions and WebSocket delivery. It creates three fixture users and two
agents with an existing agent session, but never starts an agent/model. It does not use live Dev2 data.

Run `pnpm build` once after frontend changes. Then, from the repository root,
start the fixture in one terminal (choose a fresh path if another run exists):

```sh
CHAT_BROWSER_HARNESS=/tmp/crewship-conversation-fixture.json go test ./internal/api -run '^TestWorkspaceConversationManualBrowser$' -count=1 -timeout=10m
```

Once the JSON file exists, run in a second terminal:

```sh
CHAT_BROWSER_HARNESS=/tmp/crewship-conversation-fixture.json node e2e/smoke-workspace-conversations.mjs
```

The script accepts only a fixture marked by the Go harness on a loopback URL.
It prints 37 PASS checks and the temporary screenshot directory. Coverage:
private creation/access, live two-way delivery, inbox, mute, retry identity,
revoked cached history, workspace channel admission, agent join and structured
mention, mobile send and browser errors. Direct-message coverage includes the
searchable colleague picker (self omitted), creation and reversed-pair reuse with
existing history, live reply, same-workspace third-user read/write denial, fixed
two-person People view without management controls, and mobile reopen. Unified Chat coverage uses the same New chat menu for agents,
people, groups and channels; verifies legacy human-link redirects preserve the
selection and query; loads the legacy agent slug/session through the production
static handler; checks Back/Forward and drafts across human/agent/channel threads;
and confirms a new agent session stays local without an arrival POST or new
persisted chat. The shared mobile drawer is exercised in both directions. A
mixed channel with two humans and two agents verifies one queued job per mention. The mention queues a job; model
execution remains disabled in the fixture. Additional checks cover current human
identity, activity settings persistence and member denial, Markdown internal links,
and message geometry on mobile/desktop.

Stop the fixture after the test, also when the browser fails:

```sh
touch /tmp/crewship-conversation-fixture.json.stop
```

The fixture joins its workers and removes the JSON and stop marker on exit.
The JSON contains temporary test credentials; do not commit or share it.

Continuation acceptance covers cancel without writes, a committed creation with
a simulated lost HTTP response, direct-to-group private-history boundaries, an
explicit fresh workspace channel with an agent, and the grouped agent sidebar.
