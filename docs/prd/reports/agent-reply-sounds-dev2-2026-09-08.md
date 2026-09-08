# Agent reply sounds and profile menu — Dev2, 2026-09-08

Fixes the missing audible completion of direct agent messages. The first sound
implementation explicitly excluded agent messages; only human messages and
important non-message Inbox items could sound.

## Final behavior

A successful, newly persisted agent answer now plays the selected Chat sound
once when complete, including while its direct chat is focused. Tokens, tool
activity, errors, cancellations, empty answers and history replay stay silent.
The away-from-chat Inbox projection uses the same Chat choice. Its persisted
`replied_at` timestamp and the live done frame identify the same occurrence,
so two tabs or the two delivery paths share one deduplication key. Repeated
agent replies can refresh one aggregate Inbox row and still produce a new cue.
Human chat's focused-reading suppression is unchanged.

An existing sound opt-in reactivates browser audio on the next trusted click or
keystroke after reload, including Send. This does not turn sounds on for someone
who disabled them, does not bypass browser autoplay policy and does not play a
preview. DND, volume, Off and cross-tab coordination still apply.

The standalone toolbar speaker was removed. Notification sounds now appears in
the profile/avatar menu next to Profile & Settings, linking to the existing
inline Settings → Account → Notification sounds section. The profile menu is
also available on mobile.
The profile header now reads the actual selected workspace name and membership
role instead of the old hardcoded Owner / Unify Technology labels.

## Verification

- 313 relevant frontend tests passed across 32 files, including direct agent
  completion, Inbox classification, sound coordination, trusted activation,
  profile links and Settings.
- Focused Go chatbridge/chatnotify tests verify that done and Inbox carry the
  exact persisted assistant reply timestamp.
- Production static build/TypeScript, full ESLint (zero errors, 32 existing
  warnings), Go vet, executable agent invariants and whitespace checks passed.

Deployed through the standard Dev2 service reload. Changes remain uncommitted
on the existing Chat working branch; no sibling instance was modified.
The complete `go test ./... -count=1 -timeout=30m` run passed with exit 0;
all 133 test-bearing packages succeeded, including API and database.

The first live browser run passed **6/6 checks**, with zero runtime errors.
Normal Emma authentication created a dedicated Mařena session. Two actual agent
answers were generated: `SOUND_OK` completed while the DM was focused and
`SOUND_AWAY_OK` completed after both tabs left Chat. Each produced exactly one
native Soft pop across the two tabs; the away path did not add an Inbox Chime.
Returning to history was silent. Native oscillator starts were observed in
running AudioContexts; physical speaker output was not measured.
[Live evidence](agent-reply-sounds-live-dev2-2026-09-08.json),
[focused agent chat](assets/agent-reply-sounds-dev2/focused-agent.png),
[repeatable test](../../../e2e/agent-reply-sounds-live.mjs).

The additional saved-opt-in run passed **3/3 checks**, with zero runtime errors.
After an actual reload, ordinary composer interaction unlocked audio without
Activate/Preview; the real `SOUND_REACTIVATE_OK` answer produced exactly one
native sound. The mobile profile showed Viewer and the actual workspace name,
and opened inline sound settings. Long profile names/email/workspace labels
are truncated with their complete value available as a title.
[Reactivation evidence](agent-reply-reactivation-live-dev2-2026-09-08.json),
[mobile profile menu](assets/agent-reply-sounds-dev2/mobile-profile-menu.png).

After the profile-header correction, all 33 toolbar tests across four files
passed; targeted ESLint also passed.

A final read-only mobile capture after the cosmetic reload verified the email
text element stays within the menu, uses ellipsis and retains the complete
value in its title. No extra agent prompts were sent for this check.
