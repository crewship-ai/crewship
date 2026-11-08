import type { FrameStatus } from "../../frame"
import type { DesignConnection } from "@/components/features/artifacts-design/design-provider"

/** What the Design editor knows about its live canvas, from the socket and the checkpoint mutation. */
export interface DesignFrameState {
  /** The canvas has loaded (the server answered the read). */
  ready: boolean
  /** A pinned historical version is shown instead of the live canvas. */
  version?: number
  connection: DesignConnection
  /** The server says this person may edit (`can_edit`). */
  canEdit: boolean
  /** The live canvas accepts changes from this tab right now. */
  editable: boolean
  /** Local work the server has not confirmed yet. */
  unsynced: boolean
  /** A checkpoint is on its way. */
  checkpointPending: boolean
  /** The version the server confirmed for the last checkpoint, while nothing changed since. */
  checkpointVersion?: number
  historyFull: boolean
}

/**
 * The chip for a Design canvas. Strongest first: a historical view, a full
 * history, access or refusal errors, then the socket. "Saved" only after the
 * server returned a checkpoint version and nothing changed since; while work
 * is unconfirmed it stays "Saving…". Read and Comment read "Read-only".
 */
export function designFrameStatus(state: DesignFrameState): FrameStatus | null {
  if (!state.ready) return null
  if (state.version) return { key: "readonly", label: `Version ${state.version}`, detail: "You are looking at a saved version. Open History to return to the live canvas." }
  if (state.historyFull) return { key: "unconfirmed", label: "History full", detail: "Another version could not be saved. Your changes stay in this browser; History has the recovery choices." }
  if (state.connection === "denied") return { key: "error", label: "Access changed", detail: "Your access to this Design changed. Reopen it to continue." }
  if (state.connection === "rejected") return { key: "error", label: "Changes refused", detail: "The server refused changes made in this browser (too large or invalid). They will not sync." }
  if (state.connection === "quota") return { key: "unconfirmed", label: "Storage full", detail: "Storage capacity is full. Your changes stay in this browser and nothing was deleted." }
  if (state.unsynced && !state.canEdit) return { key: "readonly", detail: "Unsent changes stay in this browser and come back when you can edit again." }
  if (state.connection === "offline") return { key: "offline", detail: "Changes stay in this browser and send when the canvas reconnects." }
  if (state.connection === "connecting") return { key: "refreshing", label: "Connecting…", detail: "Opening the live canvas." }
  if (!state.editable) return { key: "readonly" }
  if (state.unsynced || state.checkpointPending) return { key: "saving" }
  if (state.checkpointVersion) return { key: "saved", label: `Saved · v${state.checkpointVersion}` }
  return { key: "live" }
}
