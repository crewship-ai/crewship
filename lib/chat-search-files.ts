import type { ChatSearchResult } from "@/lib/chat-search"

/**
 * Proposed contract for Files search (CH-25, FE-REQ-F6-2):
 * GET /api/v1/files/search?q&cursor&type&from&after&before ->
 *   { items: FileSearchItem[], next_cursor?: string, unindexed_binary_count: number }
 * Not implemented server-side. The fixture client below stands in for it.
 */
export interface FileSearchItem {
  id: string
  name: string
  kind: "document" | "slides" | "image" | "binary"
  /** Server-extracted text only. Binary files carry no snippet. */
  snippet?: string
  indexed: boolean
  href: string
  updated_at: string
}

export const FILE_SEARCH_FIXTURES: FileSearchItem[] = [
  { id: "f1", name: "Q4 launch plan", kind: "document", snippet: "Launch checklist and owners for the Q4 release", indexed: true, href: "/files/f1", updated_at: "2026-10-12T09:00:00Z" },
  { id: "f2", name: "Pipeline overview", kind: "slides", snippet: "Deploy pipeline stages", indexed: true, href: "/files/f2", updated_at: "2026-10-08T09:00:00Z" },
  { id: "f3", name: "screenshot.png", kind: "binary", indexed: false, href: "/files/f3", updated_at: "2026-10-01T09:00:00Z" },
]

export function searchFilesFixtures(text: string): { status: ChatSearchResult["status"]; items: FileSearchItem[]; unindexed: number } {
  const q = text.trim().toLowerCase()
  if (q.length < 2) return { status: "idle", items: [], unindexed: 0 }
  const items = FILE_SEARCH_FIXTURES.filter((f) => f.indexed && (f.name.toLowerCase().includes(q) || (f.snippet ?? "").toLowerCase().includes(q)))
  // Binary files are never indexed; say so instead of pretending they were searched.
  return { status: "partial", items, unindexed: FILE_SEARCH_FIXTURES.filter((f) => !f.indexed).length }
}
