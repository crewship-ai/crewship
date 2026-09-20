import { apiFetch } from "@/lib/api-fetch"

const ROUTINE_LIST_PAGE_SIZE = 200

/** Fetch every page from an additive routine-list pagination endpoint. Older
 * servers ignore limit/offset and return the complete legacy array; the
 * short-page check keeps those deployments compatible. */
export async function fetchAllRoutinePages<T>(url: string, signal: AbortSignal): Promise<T[]> {
  const all: T[] = []
  let offset = 0
  for (;;) {
    const separator = url.includes("?") ? "&" : "?"
    const response = await apiFetch(
      `${url}${separator}limit=${ROUTINE_LIST_PAGE_SIZE}&offset=${offset}`,
      { signal },
    )
    if (!response.ok) throw new Error(`routine list: ${response.status}`)
    const page: unknown = await response.json()
    if (!Array.isArray(page)) return all
    all.push(...(page as T[]))
    const next = response.headers.get("X-Next-Offset")
    if (!next || page.length === 0) return all
    const nextOffset = Number(next)
    if (!Number.isSafeInteger(nextOffset) || nextOffset <= offset) {
      throw new Error("routine list returned an invalid pagination cursor")
    }
    offset = nextOffset
  }
}
