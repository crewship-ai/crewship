# Team Chat live acceptance on Dev2

Run after the migration/backend/frontend are deployed and
[`prepare-chat-demo.py`](prepare-chat-demo.md) created its labelled demo accounts:

```sh
python3 e2e/chat-team-live.py --report /tmp/chat-team-live-dev2.json
```

This is an **opt-in mutation of Dev2**, using the authenticated owner CLI profile
and the separately authenticated demo member. It uses local port 8082 explicitly;
it never seeds/resets a workspace or directly writes SQLite.

It enables issue/routine subscriptions on the owned demo channel, checks member
read/write ACL, creates an unassigned low-priority labelled demo issue in
`copy-site`, advances only that issue from BACKLOG to TODO, saves/runs the supplied
manual identity-transform routine (no model/network/file actions), and checks the
actual resulting trusted channel cards. Then it adds Mařena, sends one structured
mention and verifies her real model answer against the stored issue status/link.
The status question requests no tools or work mutations. Stable message and run
idempotency keys make retries reuse the original execution; a repeat does not
constitute a fresh model-capacity test. An existing demo issue moved beyond TODO
causes the script to fail rather than overwrite that state.

The channel, demo issue/routine, accounts and agent membership remain for user
exploration. Credentials stay under the private operator directory documented in
the demo preparation guide; the JSON evidence contains only synthetic resource IDs
and check results. The legacy routine save/run commands still emit human text in
JSON mode, so this harness explicitly captures their text and verifies persisted
results via structured Chat reads. Chat room commands themselves use JSON.

For isolated browser coverage without live models, see
[`workspace-conversations.md`](workspace-conversations.md). This live scenario
complements API/store/backup tests; it does not test corporate-scale capacity,
SSO, attachments in shared rooms, threads or reaction workflows.
