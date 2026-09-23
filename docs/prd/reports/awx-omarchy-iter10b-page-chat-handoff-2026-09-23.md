# AWX/Omarchy iterace 10b: Page → chat

**Base:** `feat/chat-draft-handoff` (#2670), which provides `?new=1` and unsent draft behavior. **Issue:** #2671. This branch is stacked until its base merges.

The Pages SubBar offers **Ask about this Page**. The chooser selects the owning crew's lead when available and lets the user select another agent. It opens `/chat/<agent>?page=<slug>&new=1&workspace_id=<id>`. No Page content or prompt is placed in the URL. The chat opens a fresh, unsent session, shows a removable Page chip, and attaches only `{page_context:{slug}}` to the first message if the chip remains.

The bridge treats the socket metadata as a request, not as evidence. At send time `PageHandler.ResolvePageChatContext` checks the human's current workspace membership and Page reach, the agent's current workspace membership, and a live agent Page grant whose issuer still has authority. A refusal persists no message. The server appends only Page name, slug and ID in a bounded, explicitly untrusted block and persists the server-derived `page_context` with snapshot time. This narrow context deliberately omits panel state and payload: a Page grant does not give an agent the owning crews' panel data. A history turn renders the provenance from persisted metadata.

Coverage: Go tests for both-reader authorization, revoked issuer, removed membership, missing Page/agent, forged metadata, no persistence on refusal, bounded untrusted name; Vitest for new-session binding, no auto-send, lead choice and alternate target, removable chip, metadata send and history parser. `go vet`, `tsc`, frontend checks and CI are required before merge.

Limit: the selector lists live workspace agents, including agents without a Page grant, because a member may not read the Page's full ACL. The server refuses that target when sending, with a visible message. The UI's human Page read is a helpful preview; it is not the authorization decision.
