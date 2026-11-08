// Stand-in for components/features/artifacts-design/use-design in the Artifacts library browser test:
// the real DesignEditor renders a fixed Yjs canvas without the collaboration socket. The canvas is
// connected and saved (server version 2); this person may edit; nobody else is on it. Nothing is sent.
import * as Y from "yjs"
import { designNodes, upsertDesignNode } from "../components/features/artifacts-design/design-document"

const doc = new Y.Doc()
upsertDesignNode(doc, { id: "home", type: "frame", name: "Home", x: 0, y: 0, width: 640, height: 420, fill: "#ffffff" })
upsertDesignNode(doc, { id: "hero", type: "rect", name: "Hero", parentId: "home", x: 32, y: 32, width: 576, height: 180, fill: "#e0e7ff" })
upsertDesignNode(doc, { id: "title", type: "text", name: "Title", parentId: "home", x: 56, y: 72, width: 420, height: 48, fill: "#1f2933", text: "Launch week landing" })
upsertDesignNode(doc, { id: "cta", type: "rect", name: "Call to action", parentId: "home", x: 56, y: 260, width: 180, height: 48, fill: "#4f46e5" })
const idle = { mutate() {}, mutateAsync: async () => ({ version: 2 }), isPending: false, isSuccess: false, error: null, data: undefined }
const state = {
  readRevoked: false, conflicts: [], dismissConflict() {}, setCursor() {},
  query: { data: { page_id: "page-design", can_edit: true, can_comment: true, latest_version: 2, version: 2, nodes: designNodes(doc) }, isError: false, error: null },
  versions: { data: { versions: [{ version: 1, message: "First layout" }, { version: 2, message: "Hero and call to action" }], shared_version: null } },
  comments: { data: { comments: [] } },
  doc, nodes: designNodes(doc), peers: [], connection: "connected", editable: true, dirty: false, unsynced: false, draftError: false,
  save: idle, restore: idle, share: idle, comment: idle, connectAgent: idle,
}

export function useDesign(): typeof state { return state }
