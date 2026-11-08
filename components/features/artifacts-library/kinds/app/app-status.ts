import type { ArtifactInteractiveRuntime } from "@/components/features/chat/artifact-context/projects/types"
import type { FrameStatus } from "../../frame"

/** One capability chip under the view slot, from the app's real declaration. */
export interface AppCapability {
  id: "user" | "data" | "actions"
  label: string
  detail: string
}

/**
 * What an app may use, from what the server declared for it: an interactive
 * project (`interactive.json`, served as `interactive`) gets the viewer's name
 * and the artifact's shared data collections; nothing else exists in its
 * runtime. A project without it is static and gets none.
 */
export function appCapabilities(interactive: ArtifactInteractiveRuntime | undefined, canWrite: boolean): AppCapability[] {
  if (!interactive) return []
  const collections = interactive.collections.length ? interactive.collections.join(", ") : "none declared"
  return [
    { id: "user", label: "User", detail: "Shows your name inside the app. Nothing else about you." },
    { id: "data", label: "Data", detail: `${canWrite ? "Reads and saves" : "Reads"} this artifact's shared data (${collections}). Every call is checked on the server with your access.` },
  ]
}

/** An interactive app whose server has no isolating runtime origin cannot run here; it never falls back to this site. */
export const runtimeMissing = (interactive: ArtifactInteractiveRuntime | undefined): boolean => Boolean(interactive && !interactive.runtime_url)

/**
 * The chip for an App, strongest first. Only what the snapshot proves:
 * an older version is read-only, an app that cannot run says so, a reader is
 * read-only, and a running interactive app is live. A static app on its
 * latest version reports nothing, so the chip stays hidden.
 */
export function appFrameStatus(input: { interactive?: ArtifactInteractiveRuntime; canWrite: boolean; canComment: boolean; viewingOld: boolean; shown: number }): FrameStatus | null {
  if (input.viewingOld) return { key: "readonly", label: `Viewing v${input.shown}`, detail: "An older version, read-only. Restoring it adds it as a new version." }
  if (runtimeMissing(input.interactive)) return { key: "error", label: "Can't run here", detail: "This installation has no separate application domain, so the app's own code does not run. Nothing was loaded." }
  if (!input.canWrite) return { key: "readonly", ...(input.canComment ? { label: "Can comment", detail: "You can read this app and comment on it. Changing it needs edit access." } : {}) }
  if (input.interactive) return { key: "live", label: "Running", detail: "The app runs isolated on its own origin. Its shared data is the same for everyone who can open it." }
  return null
}
