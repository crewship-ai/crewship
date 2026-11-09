import { apiFetch } from "@/lib/api-fetch"
import { schemaAllows } from "./contracts"
import { documentSurface } from "./surface"

/** The schema that adds the Docs Canvas (schema 5 plus the surface). */
export const CANVAS_SCHEMA: number = documentSurface.CANVAS_SCHEMA

/** Outcome of an explicit, human-only schema upgrade such as enabling Canvas. */
export type UpgradeResult =
  | { status: "done"; rev: number }
  /** The document changed (a newer revision, epoch or schema) after it was read: nothing changed. */
  | { status: "stale" }
  | { status: "denied" }
  | { status: "unavailable" }
  /** Network failure or an unreadable answer: the server may or may not have upgraded it; retry with the same ID. */
  | { status: "unknown" }
  | { status: "error" }

export interface UpgradeTarget { pageId: string; schema: number; rev: number; epoch: string }

/** Whether a person may be offered "Enable Canvas" for a document at this schema. */
export function canEnableCanvas(schema: number | undefined, canWrite: boolean): boolean {
  return canWrite && schema !== undefined && schema < CANVAS_SCHEMA && schemaAllows(schema, "upgrade")
}

/**
 * Upgrade the document to the Canvas schema. The same request ID must be sent
 * again after an unknown outcome: the server answers a repeat with the
 * original receipt and never upgrades twice. Content, block IDs and hashes,
 * versions and comments do not change.
 */
export async function enableCanvas(target: UpgradeTarget, requestId: string): Promise<UpgradeResult> {
  const body = { id: requestId, schema: target.schema, target_schema: CANVAS_SCHEMA, page_id: target.pageId, epoch: target.epoch, ifRev: target.rev }
  let response: Response
  try {
    response = await apiFetch(`/api/v1/pages/${encodeURIComponent(`@${target.pageId}`)}/document/upgrade`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), cache: "no-store" })
  } catch { return { status: "unknown" } }
  const value = await response.json().catch(() => null) as Record<string, unknown> | null
  if (response.ok) return typeof value?.rev === "number" && Number.isSafeInteger(value.rev) && value.schema === CANVAS_SCHEMA ? { status: "done", rev: value.rev } : { status: "unknown" }
  // A 404 may be a server without this route yet: an error, never a lost session.
  if ([401, 403].includes(response.status)) return { status: "denied" }
  if (response.status === 409) return { status: "stale" }
  if (response.status === 503) return { status: "unavailable" }
  if (response.status >= 500) return { status: "unknown" }
  return { status: "error" }
}

export function upgradeFailure(result: Exclude<UpgradeResult, { status: "done" }>): string {
  switch (result.status) {
    case "stale": return "The document changed while you were deciding. Nothing was changed; try again."
    case "denied": return "You no longer have edit access to this document. Nothing was changed."
    case "unavailable": return "Documents are unavailable right now. Nothing was changed; try again later."
    case "unknown": return "We could not confirm whether Canvas was enabled. Try again; it is never enabled twice."
    default: return "Canvas could not be enabled. Nothing was changed."
  }
}
