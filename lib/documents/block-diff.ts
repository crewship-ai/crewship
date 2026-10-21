/** Block-level comparison of two confirmed Document contents (history compare and proposal review). */

export interface DiffNode { type?: string; text?: string; attrs?: Record<string, unknown>; marks?: { type: string; attrs?: Record<string, unknown> }[]; content?: DiffNode[] }
export type DiffKind = "same" | "added" | "removed" | "changed" | "moved"
export interface BlockDiff {
  kind: DiffKind; id: string; type: string
  before?: string; after?: string
  /** Set when the text is equal but formatting or attributes differ (link target, heading level, marks, ...). */
  beforeFormat?: string; afterFormat?: string
}

const isNode = (value: unknown): value is DiffNode => value !== null && typeof value === "object" && !Array.isArray(value)

function textOf(node: DiffNode): string {
  if (typeof node.text === "string") return node.text
  const parts = (node.content ?? []).map(textOf).filter(Boolean)
  return parts.join(node.content?.some(child => child.type === "paragraph" || child.type === "tableRow" || child.type === "listItem") ? "\n" : "")
}

const stable = (value: unknown): string => JSON.stringify(value, (_key, v) => v && typeof v === "object" && !Array.isArray(v) ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => a < b ? -1 : 1)) : v)

/** Everything that is not the stable block ID: type, attributes, marks and structure. */
function signature(node: DiffNode): string {
  const strip = (n: DiffNode): unknown => {
    const { id: _id, ...attrs } = (n.attrs ?? {}) as Record<string, unknown>
    void _id
    return { t: n.type, a: attrs, m: n.marks, x: n.text, c: n.content?.map(strip) }
  }
  return stable(strip(node))
}

/** Human-readable formatting facts: node attributes (without ID) and marks, nested. */
function formatOf(node: DiffNode): string {
  const facts: string[] = []
  const walk = (n: DiffNode, path: string) => {
    const attrs = Object.entries(n.attrs ?? {}).filter(([key, value]) => key !== "id" && value !== null && value !== undefined).map(([key, value]) => `${key}=${String(value)}`)
    if (attrs.length) facts.push(`${path}${n.type ?? "node"}: ${attrs.join(", ")}`)
    if (n.marks?.length) facts.push(`${n.text ? `"${n.text.slice(0, 40)}" ` : ""}${n.marks.map(m => m.attrs && Object.keys(m.attrs).length ? `${m.type}(${Object.values(m.attrs).map(String).join(", ")})` : m.type).join(", ")}`)
    n.content?.forEach(child => walk(child, `${path}${n.type ?? "node"} > `))
  }
  walk(node, "")
  return facts.join("; ") || "no formatting"
}

interface Block { key: string; id: string; type: string; text: string; sig: string; format: () => string }

function blocks(root: unknown): Block[] {
  if (!isNode(root) || !Array.isArray(root.content)) return []
  const seen = new Map<string, number>()
  return root.content.filter(isNode).map((node, index) => {
    const sig = signature(node)
    const given = typeof node.attrs?.id === "string" && node.attrs.id ? node.attrs.id : undefined
    // Blocks without a stable ID are matched by identical content (and occurrence), never by position.
    const base = given ?? `content:${sig}`
    const n = given ? 0 : (seen.get(base) ?? 0)
    if (!given) seen.set(base, n + 1)
    return { key: given ? base : `${base}#${n}`, id: given ?? `position-${index}`, type: node.type ?? "unknown", text: textOf(node), sig, format: () => formatOf(node) }
  })
}

/** Indices (into `seq`) of one longest strictly increasing subsequence. O(n log n). */
function lis(seq: number[]): Set<number> {
  const tails: number[] = [], prev = new Array<number>(seq.length).fill(-1)
  seq.forEach((value, i) => {
    let lo = 0, hi = tails.length
    while (lo < hi) { const mid = (lo + hi) >> 1; if (seq[tails[mid]] < value) lo = mid + 1; else hi = mid }
    if (lo > 0) prev[i] = tails[lo - 1]
    tails[lo] = i
  })
  const keep = new Set<number>()
  for (let i = tails.at(-1) ?? -1; i >= 0; i = prev[i]) keep.add(i)
  return keep
}

/** Output follows the target order; removed blocks follow their nearest surviving predecessor. O(n log n). */
export function diffBlocks(base: unknown, target: unknown): BlockDiff[] {
  const before = blocks(base), after = blocks(target)
  const baseIndex = new Map(before.map((block, index) => [block.key, index]))
  const targetKeys = new Set(after.map(block => block.key))
  // Removed blocks, bucketed by the last surviving block before them in the base ("" = document start).
  const removed = new Map<string, Block[]>()
  let anchor = ""
  for (const block of before) {
    if (targetKeys.has(block.key)) { anchor = block.key; continue }
    const bucket = removed.get(anchor); if (bucket) bucket.push(block); else removed.set(anchor, [block])
  }
  // Blocks that keep their relative order are "stable"; the rest of the shared blocks moved.
  const shared = after.map((block, index) => ({ block, index })).filter(entry => baseIndex.has(entry.block.key))
  const stableAt = lis(shared.map(entry => baseIndex.get(entry.block.key)!))
  const stableKeys = new Set([...stableAt].map(i => shared[i].block.key))

  const result: BlockDiff[] = []
  const flush = (key: string) => { for (const block of removed.get(key) ?? []) result.push({ kind: "removed", id: block.id, type: block.type, before: block.text }) }
  flush("")
  for (const block of after) {
    const index = baseIndex.get(block.key)
    if (index === undefined) result.push({ kind: "added", id: block.id, type: block.type, after: block.text })
    else {
      const old = before[index]
      if (old.sig !== block.sig) {
        const textSame = old.text === block.text && old.type === block.type
        result.push({ kind: "changed", id: block.id, type: block.type, before: old.text, after: block.text, ...(textSame ? { beforeFormat: old.format(), afterFormat: block.format() } : {}) })
      } else if (!stableKeys.has(block.key)) result.push({ kind: "moved", id: block.id, type: block.type, before: old.text, after: block.text })
      else result.push({ kind: "same", id: block.id, type: block.type, after: block.text })
    }
    flush(block.key)
  }
  return result
}

export function summarizeDiff(diff: BlockDiff[]) {
  const count = (kind: DiffKind) => diff.filter(entry => entry.kind === kind).length
  return { added: count("added"), removed: count("removed"), changed: count("changed"), moved: count("moved") }
}
