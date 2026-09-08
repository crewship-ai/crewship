# Chat unification — 2026-09-07

Owner asked to integrate humans and shared rooms into the existing Chat tab,
with more scenarios and a wireframe. Specification:
[unified-chat](../unified-chat.md). Status: validated and deployed to Dev2.

## Evidence and scope

The previous 19 browser checks covered two separate surfaces. They did not
prove one unified navigation experience. This phase adds that experience and
checks its transitions rather than describing prior tests as proof of it.

Buzz's landing page was inspected in Chromium and its official README/VISION
were read. The linked live Mařena session redirected to login with an expired
session. No authenticated claim about that user's private transcript is made.
The browser fixture uses real temporary sessions and production static routing.

## Review artifacts

- [Interactive wireframe](../wireframes/chat-unified.html)
- [Desktop preview](../wireframes/chat-unified-desktop.png)
- [Mobile preview](../wireframes/chat-unified-mobile.png)

The wireframe uses sample data. Switching, draft retention, list search, the
New chat menu, mock sending and mobile layout were tested in Chromium.

## Implemented behavior

Common Chat navigation and New chat menu, in-place
agent/human selection, canonical `/chat?conversation=...` links and legacy URL
compatibility. Existing guarded human message/participant UI and agent session
facilities are reused. Private groups remain human-only; mixed channels inherit
workspace access and explicit agent membership.

Backend tests cover canonical links and two-human/two-agent explicit mentions,
with separate durable jobs and no reply-triggered loop. An additional backup
regression exposed and fixed stale inbox source identity after restored user-ID
reconciliation, which otherwise duplicated the recipient's inbox aggregate.
Agent completions in these tests are simulated; no live model call is required.

## Validation

- Full Go suite passed (`go test ./... -count=1 -timeout=30m`), including
  API, database and backup. Final `go vet ./...` passed after the fixture update.
- 660 frontend tests across 69 files passed. A subsequent label-only change
  distinguishes global New chat from agent-local New session; its 11 targeted
  tests passed and the final production frontend build passed afterward.
- ESLint: zero errors, 32 existing warnings outside this change. Diff check clean.
- Final browser acceptance passed all 27 scenarios with no uncaught JavaScript
  or dropped realtime events: actual shared sidebar/facets, new menu, two-user
  direct history, old human bookmark redirect, agent deep-link IPC history,
  no session/execution writes on arrival, distinct drafts, Back/Forward, mobile
  switching both ways and 850px navigation without horizontal overflow.
- Two agent recipients were selected through real autocomplete; the source
  message produced exactly two durable agent jobs. Completion/no-loop behavior
  has separate backend tests with simulated completions, not paid model calls.
- Early browser failures identified ambiguous test selectors and the missing
  isolated IPC fixture. Selectors now target the global menu/context header;
  the fixture uses real temporary JSONL conversation.Store history and a Unix
  IPC read endpoint through the production proxy. It exposes no execution
  endpoints. Full production Go checks ran before this test-only refinement;
  the refined fixture compiled, ran the final browser suite and passed vet.
- The temporary authenticated fixture shut down cleanly and removed its token
  JSON and stop marker. No live user's private history was fabricated or read.

Final log: `/tmp/chat-unified-browser-final.log`.
Actual UI screenshots: `/tmp/crewship-conversations-browser-1PVDWO`.
Offline/rebuild badges in fixture screenshots reflect its intentionally absent
runtime/containers, not the result of the live Dev2 healthcheck.

## Dev2 deployment

Reloaded `crewship-ws@2` successfully on 2026-09-07 after the final build.
Go on port 8082 and Next.js on port 3012 are running. Local health returned
`{"status":"ok"}`; public `/api/health` and `/chat` returned HTTP 200.
The legacy `/conversations` entry also serves successfully (HTTP 200); its
client-side redirect was verified by the authenticated browser suite above.
Unauthenticated direct-conversation creation returned HTTP 401.
The running OpenAPI document equals the checked-in generated specification.

Schema remains `20260907085939` (`workspace_direct_conversations`), with 278
applied migrations and none outstanding. This UI unification required no new
migration. Changes are deployed from the working branch and remain uncommitted;
deployment does not imply a merged or reviewed PR.
