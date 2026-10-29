# Command editor connection status

This presentation-only monitor and status component are opt-in. Nothing imports
them into the existing editor. They do not open or close sockets, request access,
change a lease, store document contents, replace an editor or send operations.

The command host integration must:

1. Create one `WordCraftConnectionMonitor(silenceMs)` per document session, with
   the server-silence limit explicitly configured. Mount its indicator beside
   the editor and replace the previous save status line. Keep the editor and its
   draft/IME state mounted during a transport failure.
2. Call `beginConnection()` when starting a transport. Keep the returned epoch
   on every callback. Call `disconnected(epoch)` on close/error; no application
   message within the limit also marks the status disconnected. A transport
   opening or heartbeat cannot restore `Uloženo`.
3. After checking the document/session, message shape and revision, pass valid
   server liveness messages as `heartbeat` and committed results as `receipt`.
   Use `snapshot` for the current authoritative state after resynchronization.
   A snapshot at the same known revision is accepted only while reconnecting;
   old revisions and duplicate receipts are ignored. Saving/unknown may upgrade
   to durable saved at the same revision; saved cannot regress through a duplicate.
   Revisions use the transport's unsigned 64-bit counter. The host must detect
   delivery gaps and resynchronize before delivering authoritative observations;
   the indicator cannot validate document content or missing operations. The
   status does not apply any content to the editor.
4. Call `retainDraft()` on local input, including composition. Keep its returned
   local version attached to the original operation in the existing host. On a
   saved server result, supply `confirmedDraftVersion` only after the server has
   reconciled the original `op_id` and confirmed all local versions through it.
   A snapshot without this evidence does not clear a retained draft. A receipt
   for an earlier local version does not clear later input. These counters are
   presentation bookkeeping, never a second command queue or source of authority.
5. Let the server reconcile unknown outcomes. Never resend pending operations,
   create new `op_id`s or relabel old operations just because transport resumed.
   Never use this status to grant edit access. Dispose the monitor at the real
   end of the document session.

`confirmedDraftVersion` must not be inferred from matching text, revision alone,
transport liveness or a locally applied edit. The public state exposes only
server revision, status and whether a local draft remains unconfirmed.

Run the fake-time, incoming-message interruption and mounted-editor tests with:

```sh
crewship-heavy pnpm exec vitest run components/features/pages/wordcraft-connection
```

These tests exercise the presentation contract without a server. Actual command
host wiring, server reconciliation and browser deployment remain integration
acceptance boundaries; this component by itself does not establish them.
