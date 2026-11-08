import type { SaveDurability } from "@/lib/editor-lifecycle/save-state"
import type { FrameStatus } from "../../frame"

/**
 * What the Docs chip reads: the native document session as `usePageDocument`
 * reports it (status words, queue, recovery), plus the two facts only the
 * view knows (a restore in flight, and whether this tab sent a write).
 */
export interface DocsSyncFacts {
  /** The confirmed state is hydrated and the editor is bound. */
  hydrated: boolean
  denied: boolean
  /** The documents service admitted this session (availability). */
  available: boolean
  restoring: boolean
  /** The session's own status words ("Saved", "Saving…", "Disconnected…"). */
  status: string
  pending: number
  recovery: boolean
  recoveredDrafts: number
  canWrite: boolean
  durability: SaveDurability
  /** This tab queued a write in this session; a later "Saved" is that write's receipt. */
  sentThisSession: boolean
}

/**
 * The status chip for a Docs page. Strongest first: access, a restore in
 * flight, a draft that needs the person, then the connection, then the save.
 * "Saved" appears only once the session has confirmed a write this tab sent
 * (the hook says "Saved" only after the receipt and a fresh read); before any
 * write the page is Live. Nothing is invented: unknown words stay in detail.
 */
export function docsFrameStatus(facts: DocsSyncFacts): FrameStatus | null {
  if (facts.denied) return { key: "error", label: "No access", detail: facts.status }
  if (!facts.hydrated) return null
  if (facts.restoring) return { key: "saving", label: "Restoring…", detail: "Waiting for the server to confirm the restore. Editing is paused until it does." }
  if (facts.recoveredDrafts > 0) return { key: "draft" }
  if (/^Save confirmation unavailable/.test(facts.status)) return { key: "unconfirmed" }
  if (facts.recovery) return { key: "draft", label: "Unsaved draft", detail: "Your changes are not saved. Download them before you reopen the document." }
  if (!facts.available) return { key: "offline", detail: "Documents are unavailable. Your changes stay in this tab until the service is back." }
  if (/^(Disconnected|Temporarily rate limited)/.test(facts.status)) {
    return { key: "offline", ...(facts.pending > 0 ? { detail: `${facts.pending === 1 ? "1 change waits" : `${facts.pending} changes wait`} here. Keep this tab open until they are sent.` } : {}) }
  }
  if (facts.pending > 0) {
    return facts.durability === "unsaved"
      ? { key: "saving", detail: "Your last change is on its way. Until the server confirms it, it exists only in this tab." }
      : { key: "saving" }
  }
  if (!facts.canWrite) return { key: "readonly", detail: "You can read this document. Ask its owner for edit access." }
  if (facts.status === "Saved") return facts.sentThisSession ? { key: "saved" } : { key: "live" }
  if (facts.status === "Loading document…") return null
  return { key: "error", label: "Needs attention", detail: facts.status }
}

/** A ProseMirror JSON node, as `editor.getJSON()` returns it. */
export interface DocNode {
  type?: string
  text?: string
  attrs?: Record<string, unknown>
  content?: DocNode[]
}

export interface OutlineEntry {
  /** The block id the editor keeps on every heading (`data-block-id`); null before it is assigned. */
  id: string | null
  level: 1 | 2 | 3
  text: string
}

const textOf = (node: DocNode): string => node.text ?? (node.content ?? []).map(textOf).join("")

/** The document's headings in order: the outline navigator. Empty headings are left out. */
export function outlineOf(doc: DocNode | null | undefined): OutlineEntry[] {
  const entries: OutlineEntry[] = []
  for (const node of doc?.content ?? []) {
    if (node.type !== "heading") continue
    const text = textOf(node).trim()
    if (!text) continue
    const level = Number(node.attrs?.level)
    entries.push({ id: typeof node.attrs?.id === "string" ? node.attrs.id : null, level: level === 1 || level === 2 ? level : 3, text })
  }
  return entries
}

const blockText = (node: DocNode): string[] => node.text !== undefined ? [node.text] : (node.content ?? []).flatMap(child => [...blockText(child), " "])

/** Words and minutes to read (220 words a minute, at least one once there is any text). */
export function readingStats(doc: DocNode | null | undefined): { words: number; minutes: number } {
  const words = blockText(doc ?? {}).join("").split(/\s+/).filter(Boolean).length
  return { words, minutes: words === 0 ? 0 : Math.max(1, Math.round(words / 220)) }
}

/** "640 words · 3 min read"; "No words yet" for an empty page. */
export function readingText({ words, minutes }: { words: number; minutes: number }): string {
  if (words === 0) return "No words yet"
  return `${words.toLocaleString("en-US")} ${words === 1 ? "word" : "words"} · ${minutes} min read`
}
