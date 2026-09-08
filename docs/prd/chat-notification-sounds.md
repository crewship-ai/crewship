# Personal Chat and Inbox sounds

Add a restrained sound palette to Crewship's existing Chat/Inbox without changing
notification delivery or workspace permissions. Sound is an optional companion to
existing badges and readable notifications, never their replacement.

## Controls and defaults

Settings → Account → Notification sounds (`/settings?tab=sounds`) contains the
inline personal controls on desktop and mobile for every workspace role. The
profile menu contains a Notification sounds shortcut to this same Settings
section on desktop and mobile; there is no standalone speaker button. It offers Enable/Disable,
Do not disturb, 0–100% volume, separate Chat/Inbox choices including Off, and
explicit preview buttons. Defaults: disabled until enabled, 35% volume,
Soft pop for Chat and Chime for Inbox. DND suppresses automatic cues but permits
an intentional preview. Settings belong to the current account/workspace in this
browser; they are not an administrator's workspace-wide policy or cross-device sync.

Five original synthesized presets are bundled as Web Audio parameters: Soft pop,
Glass, Chime, Inbox drop and Attention. They use short attack/release envelopes,
last less than one second, do not overlap, and fetch no third-party asset. No
stock recording or external sound license is needed.

## Eligibility

Realtime frames are invalidations, not audible events. `conversation.updated`
contains only a conversation ID and also occurs for read, mute and membership
changes. The client re-reads that conversation and its latest message page via
the normal workspace-scoped API. Only a fresh human-authored `kind=message`,
from somebody else, above the caller's read cursor in an unmuted room qualifies.
Join notices and routine activity do not qualify as human chat. Direct agent
sessions have a separate completion cue: a new successful persisted assistant
answer sounds once after its text finishes, including in the focused agent chat.
Tokens, tool output, cancellation, errors and historical replay stay silent.
Live `done` and the agent reply Inbox projection share the exact persisted
`replied_at` timestamp and canonical `agent-reply:<chat>:<timestamp-ms>` key.
Agent reply Inbox rows use the Chat choice, not the important-Inbox choice;
a new reply can refresh the same aggregate row without losing its sound.

Other Inbox candidates are authorized API rows with effective state unread, no missing
source, kind other than message, and either blocking or high/urgent priority.
Ordinary routine results stay quiet. Conversation inbox projections are excluded
entirely, so a human message cannot produce a second cue through Inbox.

No sound is played on initial data load. Only records created after this listener
was mounted or reset and less than 60 seconds old qualify; reconnect and preference
changes reset the cutoff and cancel in-flight requests. Failed/revoked reads are
silent. This intentionally avoids replaying a backlog. A recurring Inbox row
reusing an existing ID is not promised a new per-occurrence sound: the current
wire format lacks a separate occurrence revision.

## Coordination and browser constraints

One global listener lives under the dashboard RealtimeProvider. Scoped Web Locks
serialize a bounded localStorage ledger across tabs, using actual message/Inbox
IDs rather than client-generated IDs. The ledger marks before playback. A two
second cooldown collapses bursts; suppressed events are consumed, not queued.
Shared-storage or lock failure disables automatic sound rather than risking
multiple alerts. Preview remains independent.

The actual conversation component registers whether its transcript is at the
bottom. Focused, visible reading in any live tab suppresses that conversation's
cue. Read marking itself also requires focus. Heartbeats expire after 15 seconds;
unmount removes presence. No message content or credentials enter these keys.

Audio starts only from a user interaction. Enable, Activate audio and Preview
activate it explicitly; an already saved opt-in also reactivates on the next
trusted click or keystroke, including Send after a reload.
An event never creates or resumes an AudioContext. The Settings section reports blocked or
unsupported automatic audio honestly. Reload or device suspension may require
another interaction. A closed browser, suspended mobile page or disconnected
client cannot be promised an audible alert; OS/background push is a separate
feature. See [Web Audio autoplay](https://developer.mozilla.org/en-US/docs/Web/Media/Guides/Autoplay)
and [Web Locks](https://developer.mozilla.org/en-US/docs/Web/API/Web_Locks_API).

## Acceptance

Test five bounded previews, corrupted/unavailable storage, per-user/workspace
preferences, denied autoplay, DND/volume/off, human versus agent/system authors,
read cursor/mute, unauthorized API responses, initial/reconnect history, aborted
workspace changes, burst grouping and one cue across tabs. Exercise normal demo
account login and actual human messages on Dev2; distinguish instrumented browser
playback verification from subjective physical listening.

Deployment and evidence: [Dev2 verification, 2026-09-08](reports/notification-sounds-dev2-2026-09-08.md).
