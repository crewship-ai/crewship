# Personal notification sounds — Dev2, 2026-09-08

**Settings placement follow-up:** the controls now live inline at
`/settings?tab=sounds`, under Settings → Account → Notification sounds.
The toolbar speaker is a link to this section, not a separate modal editor.
All workspace roles can open it. The same scoped preferences and audio engine
are retained. Placement validation: 88 Settings/toolbar/control tests passed;
production build, lint, Go vet and whitespace checks passed. The earlier modal
screenshots below record the initial deployment, before this follow-up.
Live placement verification passed 5/5 scenarios as Emma VIEWER, with zero
browser errors: Settings navigation/deep link, inline controls, native preview,
preference retention across navigation/reload and mobile section selection.
[Current desktop](assets/sounds-settings-dev2/desktop.png),
[current mobile](assets/sounds-settings-dev2/mobile.png),
[placement evidence](notification-sound-settings-live-dev2-2026-09-08.json).

Implements [Chat and Inbox sounds](../chat-notification-sounds.md) in the existing
working tree (`fix/chat-workspace-foundations`, base `56969d7e`). The production
static export was built and the Dev2 service reloaded. No sibling instance was
changed; work remains uncommitted alongside the earlier Chat implementation.

## User-visible behavior

The speaker button in the top toolbar opens Notification sounds on desktop and
mobile, including for VIEWER. Enable sounds activates browser audio. Choose
Soft pop, Glass, Chime, Inbox drop or Attention separately for Chat and Inbox,
or Off. Volume and Do not disturb apply to automatic alerts; explicit previews
remain available. Defaults are 35%, Soft pop for Chat and Chime for Inbox.

Settings are scoped to the account/workspace on this browser. The dialog reports
when activation is required, or when shared storage/tab coordination is missing.
Sounds work while a capable browser page is running; this does not add operating
system push notifications or promise sound from closed/suspended applications.

The original tones use native oscillators with bounded gain envelopes. No remote
sound service, stock file, new dependency, API route or migration was added.

## Correctness

The existing WS payloads describe invalidation, not a sound-worthy message.
The listener fetches authorized room/message or Inbox state before deciding.
It excludes self/agent/activity/join messages, muted/read conversations,
ordinary Inbox events, conversation Inbox projections and old history. A fresh
important Inbox row means unread and blocking or high/urgent, excluding message
rows and missing sources. Reconnect, activation and preference changes discard
backlogs and invalidate in-flight work.

Web Locks serialize a bounded event ledger shared across tabs. One canonical
message cannot also ring through Inbox. A two-second cooldown groups bursts;
there is no playback queue. The actual transcript reports whether it is at the
bottom; focused reading in another tab suppresses sound. Read marking now also
requires document focus instead of merely visibility.

## Verification

- Final relevant frontend regression: **164 tests in 16 files passed**.
- Production `pnpm build` passed, including TypeScript/static export.
- Full ESLint passed with zero errors and 32 existing warnings.
- Go vet, executable agent invariants and whitespace checks passed.

## Live browser evidence

Normal Thomas and Emma password/CSRF logins, using the previously seeded demo
accounts, exercised **12 scenarios successfully**, with zero browser errors.
A dedicated private Thomas/Emma group contains the clearly labeled QA messages.
No agent runs, fake approvals or real work changes were created.

All five previews started real native oscillators in running AudioContexts.
A new human message delivered through the real WebSocket played exactly one
Soft pop across two Emma tabs. Own messages, Disable, DND, preset Off, volume 0,
personal room mute and active focused reading were silent. Initial history,
reload and renewed activation did not replay older messages. Desktop and mobile
settings rendered successfully.

The browser instrumentation calls the original OscillatorNode.start and records
its context state; it does not replace audio or bypass autoplay. Physical speaker
audibility and subjective tone preference were not measured.

[Sanitized live evidence](notification-sounds-live-dev2-2026-09-08.json),
[desktop](assets/notification-sounds-dev2/desktop.png),
[mobile](assets/notification-sounds-dev2/mobile.png).
Repeatable scenario: [notification-sounds-live.mjs](../../../e2e/notification-sounds-live.mjs).

The isolated Inbox browser scenario passed **6/6 checks**, with zero browser
errors. Real Emma authentication, native WebSocket handshake and native audio
were retained, while Inbox rows and invalidation frames were supplied only in
the browser. Fresh important rows played the selected Chime; duplicates,
old/read/ordinary/chat rows and DND remained silent. No Inbox row was inserted
into the server. [Fixture evidence](notification-sounds-inbox-dev2-2026-09-08.json)
and [repeatable script](../../../e2e/notification-sounds-inbox.mjs).

The full `go test ./... -count=1 -timeout=30m` log contains successful results
for all **133 test-bearing packages**, with no FAIL markers. A separate `go list`
enumeration was compared to the successful package names and matched exactly.
The terminal harness reported exit 143 when reaped after producing these results;
this is recorded rather than claiming an observed exit 0. Go vet exited 0.
Final diff checks and public `/api/health` (`status: ok`) passed.
