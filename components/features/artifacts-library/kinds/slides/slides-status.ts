import type { FrameStatus } from "../../frame"

/** What the Slides detail knows from the artifact snapshot the server returned. */
export interface SlidesFrameState {
  /** The server answered with a version of this deck. */
  ready: boolean
  /** Edit on this artifact (`can_write`). */
  canWrite: boolean
  /** The version on screen and the newest one; they differ while an older version is open. */
  shown: number
  latest: number
  /** The last poll failed while an earlier answer is still on screen. */
  refreshFailed: boolean
}

/**
 * The chip for a deck. Every version on screen came from the server, so the
 * newest one reads "Saved · vN" for an editor; Read and Comment read
 * "Read-only". An older version says which one; a failed poll says the view
 * may be behind.
 */
export function slidesFrameStatus(state: SlidesFrameState): FrameStatus | null {
  if (!state.ready) return null
  if (state.refreshFailed) return { key: "stale", label: "Not refreshed", detail: "The last check for new versions failed. You see the last version the server sent." }
  if (state.shown !== state.latest) return { key: "readonly", label: `Version ${state.shown}`, detail: `You are looking at version ${state.shown}. The latest is v${state.latest}; open History to return.` }
  if (!state.canWrite) return { key: "readonly" }
  return { key: "saved", label: `Saved · v${state.latest}`, detail: "The server keeps this as the latest version. Agents publish new versions; nothing is lost." }
}
