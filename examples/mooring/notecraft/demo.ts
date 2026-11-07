/** Local demo: two people on one in-memory server, side by side. Not shipped
 * in the product; see docs/development/mooring-sdk.md for how to run it. The
 * demo plays the host, so it hands NoteCraft the host's save lifecycle. */
import type { RecoveryDraft } from "../../../lib/mooring/sdk"
import { createSingleEditorSave } from "../../../lib/mooring/single-editor-save"
import { createFakeCraftServer, memoryJournal } from "./fake-server"
import { mountNoteCraft } from "./notecraft"

const server = createFakeCraftServer("Ahoj z NoteCraft.")
const rows = new Map<string, RecoveryDraft>()
const people = [{ actor_id: "u-alice", display_name: "Alice", kind: "human" as const }, { actor_id: "u-bob", display_name: "Bob", kind: "human" as const }]
const apps = people.map((actor, i) => {
  const client = server.connect(actor)
  return mountNoteCraft(document.getElementById(`pane-${i}`)!, { scope: { workspaceId: "demo", pageId: "note", sessionId: `tab-${i}`, enginePin: "demo" }, mayEdit: true, document: client, versions: client.versions, openJournal: async () => memoryJournal(rows), createSave: createSingleEditorSave })
})
// Stand-in for the host's lease event stream.
setInterval(() => { for (const app of apps) void app.refresh() }, 1000)
