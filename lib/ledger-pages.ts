// Reading a whole ledger window (#3017). The work and delivery lists are
// keyset paginated oldest first, so a window is only whole once every page of
// it is read; past LEDGER_MAX_PAGES the view says it is capped.

/** Pages a ledger view reads before it says it is capped (100 rows each). */
export const LEDGER_MAX_PAGES = 10

export async function readLedgerPages<T>(
  read: (url: string) => Promise<{ items: T[]; next_cursor: string | null }>,
  url: string,
): Promise<{ items: T[]; capped: boolean }> {
  const items: T[] = []
  let after: string | null = null
  for (let page = 0; page < LEDGER_MAX_PAGES; page++) {
    const sep = url.includes("?") ? "&" : "?"
    const next = await read(after ? `${url}${sep}after=${encodeURIComponent(after)}` : url)
    items.push(...next.items)
    if (!next.next_cursor) return { items, capped: false }
    after = next.next_cursor
  }
  return { items, capped: true }
}
