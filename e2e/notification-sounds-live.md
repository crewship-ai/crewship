# Live notification sound acceptance — Dev2

Run after frontend deployment with credentials from `crewship seed team-chat`:

```sh
TEAM_CHAT_STATE=/private/path/accounts.json node e2e/notification-sounds-live.mjs
```

The script is restricted to public Dev2 and authenticates Thomas and Emma through
normal CSRF/password login. It respects authentication `Retry-After`; passwords,
cookies and browser traces are never printed or saved. Thomas creates a clearly
labelled private QA group with Emma. Test messages remain there as synthetic QA
data; no real tasks, agent runs or workspace-wide notifications are generated.

Two Emma tabs share the same ordinary browser context and local settings. Profile menu → Notification sounds opens the inline Settings section via client
navigation. Enable/Activate/Preview clicks unlock audio; returning through browser
history preserves the page’s AudioContext. Instrumentation only wraps the native
`OscillatorNode.start` method, calls the original implementation, and records node
starts and native context state. It does not replace audio with mocks or change
browser autoplay policies. This proves functioning Web Audio playback requests,
not physical speaker audibility on a headless server.

Acceptance covers five real preset previews, one cue across two recipient tabs
for a live human message, silent initial/reloaded history, own messages, disabled
sounds, DND, Chat Off, zero volume, personal conversation mute and active focused
reading. It also captures the settings on desktop/mobile and checks runtime
errors. Notification delivery uses real server WebSocket events and API reads.
Important non-chat Inbox alerts are covered by the isolated policy/unit tests;
this live test does not manufacture approvals or escalations in the workspace.

Screenshots: `Artifacts: …` run directory.
Report without credentials: `notification-sounds-live-report.json` inside the private run directory printed as `Artifacts: …`.

Each run creates a unique mode-0700 artifact directory. Reports and screenshots stay there; inspect and sanitize them before copying selected evidence into tracked documentation. Credential state must be a regular mode-0600 file owned by the current user; symlinks are rejected.
