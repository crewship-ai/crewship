# Seeded team live validation (Dev2 only)

After `crewship seed team-chat` completes, use the `protected_state` path from its
JSON output:

```sh
TEAM_CHAT_STATE=/private/path/accounts.json node e2e/team-chat-live.mjs
```

The script targets `https://crewship-dev2.unifylab.cz` only. It logs in separately
as each of the six fictional colleagues through normal CSRF/password endpoints.
It never prints passwords or saves browser cookies/traces. Authentication 429s
respect `Retry-After`, with progress messages every at most 20 seconds during
waits; persistent rate limits stop the run instead of weakening controls.

Checks cover the real ADMIN/MANAGER/MEMBER/VIEWER assignments, six unique served
PNG images matched against bundled SHA-256 hashes, author/avatar metadata, six
planning messages plus the owner's introduction, private DM isolation and safe
RBAC probes against nonexistent resources or invalid provisioning input. Chat
send permission is verified by retrying each user's existing message with its
original client ID and content, so no new messages are added. The script does
not create issues, invite users, run models or alter activity subscriptions.

VIEWER is a workspace role, not a read-only Chat role: participant-authorized
Chat writes and self-service profile/avatar operations remain available. The
People dialog's `member` label describes conversation membership; workspace
ADMIN/MANAGER/MEMBER/VIEWER are checked using the workspace member API.

The normally authenticated Emma browser renders the public channel and checks
loaded portraits, the People dialog, mobile overflow and uncaught errors.
Screenshots go to `Artifacts: …` run directory; a report without
credentials goes to `chat-seed-team-live-validation.json` inside the private run directory printed as `Artifacts: …`. Login sessions
and normal read cursors are the only fresh state created by validation.

Each run creates a unique mode-0700 artifact directory. Reports and screenshots stay there; inspect and sanitize them before copying selected evidence into tracked documentation. Credential state must be a regular mode-0600 file owned by the current user; symlinks are rejected.
