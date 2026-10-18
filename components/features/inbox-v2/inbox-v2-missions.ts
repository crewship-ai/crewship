import { apiFetch } from "@/lib/api-fetch"
import type { Mission } from "@/lib/types/mission"

const MISSIONS_PAGE_SIZE = 100

/** Walk GET /api/v1/missions page by page until a short page. */
export async function fetchAllMissions(workspaceId: string, signal?: AbortSignal): Promise<Mission[]> {
  const rows: Mission[] = []
  const seen = new Set<string>()
  let offset = 0
  do {
    const res = await apiFetch(
      `/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId)}&limit=${MISSIONS_PAGE_SIZE}&offset=${offset}&include_tasks=true`,
      { signal },
    )
    if (!res.ok) throw new Error(`mission signals: ${res.status}`)
    const page = (await res.json()) as Mission[]
    const fresh = page.filter((m) => !seen.has(m.id))
    for (const m of fresh) seen.add(m.id)
    rows.push(...fresh)
    if (page.length < MISSIONS_PAGE_SIZE) break
    // A full page that taught us nothing new means the offset is not being
    // honoured: stop instead of re-requesting it forever.
    if (fresh.length === 0) break
    offset += page.length
  } while (true)
  return rows
}
