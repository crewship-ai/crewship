# Live private-to-team Chat acceptance (Dev2)

After the Dev2 demo accounts and continuation migration are ready, run:

```sh
python3 e2e/chat-continuation-live.py --report /tmp/chat-continuation-live-dev2.json
```

This opt-in scenario uses the normal authenticated Dev2 owner CLI and the separate
Klára/Tomáš profiles in the protected demo directory. It creates/reuses a labelled
private group from the owner's Klára DM, exchanges three distinctly authored demo
messages, and creates/reuses an explicitly workspace-visible channel with Mařena.
It then sends one real structured mention as Klára, checks the real issue status
against the agent reply, and verifies that the original private history remains
unchanged and inaccessible to Tomáš. Joining alone must not execute an agent.

It requires the existing demo issue COP-1 and agent ma-ena. It keeps the new rooms
for review; it never changes real issue state, copies private history or writes
directly to SQLite. Stable operation IDs make subsequent runs open the same rooms
and agent job. If COP-1 changes later, the stored original reply may no longer
match and the repeat fails rather than silently counting stale output as current.
No credentials are emitted in the report.

The isolated browser suite additionally covers cancelled dialogs and a committed
creation whose HTTP response is replaced with 503 before a safe UI retry.
