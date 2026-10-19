import type { LinkedViewerTarget } from "./linked-viewer-targets"

type Pinned = Extract<LinkedViewerTarget, { mode: "revision" }>

/** Body of the checkpoint reader contract: GET /pages/{page}/document/checkpoints/{checkpoint}. */
export interface CheckpointBody { page_id: string; id: string; rev: number; schema: number; content: unknown }

export type ExactRevisionRead =
  | { status: "loading" }
  | { status: "ready"; target: Pinned; schema: number; content: unknown }
  /** The pinned revision cannot be shown. The viewer never substitutes the latest content. */
  | { status: "unavailable" }
  | { status: "denied" }
  /** A temporary failure; not a revoke and not "unavailable". */
  | { status: "error" }

export interface CheckpointFetcher {
  (path: string, signal?: AbortSignal): Promise<{ status: number; body: unknown }>
}

/** Exact reads use the checkpoint reader only. The document endpoint ignores a revision query and returns latest. */
export const checkpointPath = (target: Pinned) =>
  `/api/v1/pages/${encodeURIComponent("@" + target.pageId)}/document/checkpoints/${encodeURIComponent(target.checkpointId)}`

/** Accepts a response only when page, checkpoint and revision all match the pin. */
export function acceptCheckpoint(target: Pinned, body: unknown): ExactRevisionRead {
  const value = body as Partial<CheckpointBody> | null
  if (!value || value.page_id !== target.pageId || value.id !== target.checkpointId || value.rev !== target.revision
    || !Number.isSafeInteger(value.schema) || value.content === undefined || value.content === null) return { status: "unavailable" }
  return { status: "ready", target, schema: value.schema as number, content: value.content }
}

export async function readExactRevision(fetcher: CheckpointFetcher, target: Pinned, signal?: AbortSignal): Promise<ExactRevisionRead> {
  try {
    const response = await fetcher(checkpointPath(target), signal)
    if (response.status === 401 || response.status === 403) return { status: "denied" }
    if (response.status === 404 || response.status === 410) return { status: "unavailable" }
    if (response.status !== 200) return { status: "error" }
    return acceptCheckpoint(target, response.body)
  } catch {
    return { status: "error" }
  }
}
