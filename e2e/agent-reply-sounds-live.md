# Live agent reply sounds — Dev2

```sh
TEAM_CHAT_STATE=/private/path/accounts.json node e2e/agent-reply-sounds-live.mjs
```

This explicitly starts two real Mařena replies in a dedicated session as the
fictional Emma account: `Reply exactly SOUND_OK` and `Reply exactly SOUND_AWAY_OK`.
It creates no issues, changes no agent configuration and uses normal CSRF/password
authentication. Credentials are never printed, exported as browser storage or
copied from the owner. Server rate limits remain enforced.

Two Emma tabs share one browser context. Profile menu → Notification sounds opens
the ordinary inline Settings section via client navigation, preserving each
page's native AudioContext. Native oscillator starts are observed without mocking
audio or overriding browser autoplay policy. The focused reply must play exactly
one Chat cue across two tabs. For the second reply both tabs leave Chat before
completion, so the authorized Inbox projection must produce one Chat cue instead
of a duplicate Inbox chime. Returning to history remains silent. The profile-menu
shortcut is also exercised on mobile.

This measures native audio playback requests, not physical speaker audibility.
Report: `agent-reply-sounds-live-report.json` inside the private run directory printed as `Artifacts: …`.
Screenshots: `Artifacts: …` run directory.

Each run creates a unique mode-0700 artifact directory. Reports and screenshots stay there; inspect and sanitize them before copying selected evidence into tracked documentation. Credential state must be a regular mode-0600 file owned by the current user; symlinks are rejected.
