"use client"

import * as React from "react"
import * as Y from "yjs"
import { FileText, Maximize, MessageSquare, Minus, Pencil, Plus, Redo2, StickyNote, Trash2, Undo2, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { cn } from "@/lib/utils"
import { fitAll, focusRect, moved, nextPlacement, nudged, pan, resized, toWorld, wheelFactor, zoomAt, type Handle, type Rect, type Viewport } from "@/lib/documents/canvas-viewport"
import { documentSurface, type SurfaceItem, type SurfacePlacement, type SurfaceView } from "@/lib/documents/surface"
import type { DocumentComment } from "@/hooks/use-document-comments"

/**
 * The Canvas view of one Docs document (R71 rev 2 §0.4). It shows the same
 * Y.Doc as the Page: a card is a placement of a Page block, never a copy of
 * it. Pan, zoom, fit and focus are local; only a completed move, resize, add or
 * removal writes, once, as the person's own origin so their undo stays theirs.
 *
 * Read-only people can pan, zoom, fit, focus and select; they cannot move,
 * resize, add or remove anything. Pointer: background drag pans with any
 * button; on a card the primary button moves (Edit only) and the others pan.
 * Ctrl/⌘ + wheel or a pinch zooms around the pointer; a plain wheel pans.
 *
 * "Remove from canvas" deletes only the placement: the block stays in the
 * Page. "Delete block" deletes it from the Page and the Canvas in one change,
 * after a confirmation that names both and the comment threads it detaches.
 * Undo and Redo here take back only this person's own Canvas changes.
 */
export interface DocsCanvasProps {
  doc: Y.Doc
  editable: boolean
  /** The person's own transaction origin (for example their session ID), tracked by their undo manager. */
  origin: string
  /** Open this block in the Page view (double-click or Enter on a card). */
  onOpenInPage?: (blockId: string) => void
  /**
   * The document's comment threads, as the Page shows them. Comments anchor to
   * blocks (R71 §0.4/4), never to placements: a card shows the open threads of
   * its block and everything nested in it, so a move keeps them and switching
   * views never creates a second thread.
   */
  comments?: readonly DocumentComment[]
  /** Open these threads (the same ones the Page lists) in the Comments drawer. */
  onOpenComments?: (blockId: string, threadIds: string[]) => void
}

type Drag =
  | { kind: "pan"; pointer: number; x: number; y: number }
  | { kind: "move" | "resize"; pointer: number; id: string; x: number; y: number; start: Rect; handle?: Handle }

/** One run of a block's text with the Page marks it carries (schema 4/5 formatting). */
export interface TextRun { text: string; bold?: boolean; italic?: boolean; strike?: boolean; underline?: boolean; code?: boolean; link?: boolean; color?: string; highlight?: string }
interface BlockSummary { id: string; type: string; text: string; runs: TextRun[]; level?: number }

const PALETTE = new Set(["gray", "red", "orange", "yellow", "green", "blue", "purple", "pink"])
const paletteColor = (value: unknown): string | undefined => {
  const color = (value as { color?: unknown } | null)?.color
  return typeof color === "string" && PALETTE.has(color) ? color : undefined
}

/** The block's text as formatted runs, joined like plain(): child blocks are separated by a space. */
function runsOf(node: Y.XmlElement | Y.XmlText): TextRun[] {
  if (!isElement(node)) {
    return (node as Y.XmlText).toDelta().flatMap((part: { insert?: unknown; attributes?: Record<string, unknown> }) => {
      if (typeof part.insert !== "string" || !part.insert) return []
      const marks = part.attributes ?? {}
      const run: TextRun = { text: part.insert }
      if (marks.bold) run.bold = true
      if (marks.italic) run.italic = true
      if (marks.strike) run.strike = true
      if (marks.underline) run.underline = true
      if (marks.code) run.code = true
      if (marks.link) run.link = true
      const color = paletteColor(marks.textColor), highlight = paletteColor(marks.highlight)
      if (color) run.color = color
      if (highlight) run.highlight = highlight
      return [run]
    })
  }
  return node.toArray().flatMap((child, index) => [...(index > 0 ? [{ text: " " }] : []), ...runsOf(child as Y.XmlElement)])
}

/** A card shows the Page block's formatting with the same palette as the Page editor; links are not followable on the canvas. */
function FormattedRuns({ runs }: { runs: TextRun[] }) {
  return <>{runs.map((run, index) => {
    let node: React.ReactNode = run.text
    if (run.code) node = <code className="rounded bg-muted px-1 font-mono text-[0.9em]">{node}</code>
    if (run.bold) node = <strong>{node}</strong>
    if (run.italic) node = <em>{node}</em>
    if (run.strike) node = <s>{node}</s>
    if (run.underline || run.link) node = <u>{node}</u>
    if (run.highlight) node = <mark data-highlight={run.highlight}>{node}</mark>
    if (run.color) node = <span data-text-color={run.color}>{node}</span>
    return <React.Fragment key={index}>{node}</React.Fragment>
  })}</>
}

const NOTE_TONES: Record<string, string> = {
  default: "bg-card", yellow: "bg-warn/15", green: "bg-success/15", blue: "bg-primary/10", red: "bg-destructive/10", purple: "bg-accent",
}

function plain(node: Y.XmlElement | Y.XmlText | Y.XmlFragment): string {
  if ("toDelta" in node && typeof (node as Y.XmlText).toDelta === "function" && !("nodeName" in node)) {
    return (node as Y.XmlText).toDelta().map((part: { insert?: unknown }) => typeof part.insert === "string" ? part.insert : "").join("")
  }
  return (node as Y.XmlElement).toArray().map(child => plain(child as Y.XmlElement)).join(" ")
}

/** An XmlElement (XmlText also has attributes, but no children or node name). */
const isElement = (node: unknown): node is Y.XmlElement => typeof (node as Y.XmlElement)?.nodeName === "string" && typeof (node as Y.XmlElement).toArray === "function"

/** Every block ID mapped to the top-level Page block that contains it (itself when top-level). */
export function topLevelOf(doc: Y.Doc): Map<string, string> {
  const owner = new Map<string, string>()
  const visit = (node: Y.XmlElement, top: string) => {
    const id = node.getAttribute("id")
    if (typeof id === "string") owner.set(id, top)
    for (const child of node.toArray()) if (isElement(child)) visit(child, top)
  }
  for (const node of doc.getXmlFragment("default").toArray()) {
    if (!isElement(node)) continue
    const id = node.getAttribute("id")
    if (typeof id === "string") visit(node, id)
  }
  return owner
}

/** Open threads per top-level block: roots only, resolved and detached ones excluded. */
export function threadsByBlock(doc: Y.Doc, comments: readonly DocumentComment[]): Map<string, string[]> {
  const owner = topLevelOf(doc), result = new Map<string, string[]>()
  for (const comment of comments) {
    if (comment.parent || comment.resolved || !comment.block || comment.status === "detached" || comment.status === "resolved") continue
    const top = owner.get(comment.block)
    if (!top) continue
    result.set(top, [...(result.get(top) ?? []), comment.id])
  }
  return result
}

/** Top-level Page blocks with their plain text and formatted runs, in Page order. */
export function pageBlocks(doc: Y.Doc): BlockSummary[] {
  return doc.getXmlFragment("default").toArray().flatMap(node => {
    if (!isElement(node)) return []
    const element = node
    const id = element.getAttribute("id")
    if (typeof id !== "string") return []
    const level = element.getAttribute("level") as unknown
    return [{ id, type: element.nodeName, text: plain(element).replace(/\s+/g, " ").trim(), runs: runsOf(element), ...(typeof level === "number" ? { level } : {}) }]
  })
}

function useSurface(doc: Y.Doc): { view: SurfaceView | null; blocks: BlockSummary[]; error: boolean } {
  const read = React.useCallback(() => {
    try { return { view: documentSurface.project(doc) as SurfaceView, blocks: pageBlocks(doc), error: false } }
    catch { return { view: null, blocks: pageBlocks(doc), error: true } }
  }, [doc])
  const [state, setState] = React.useState(read)
  React.useEffect(() => {
    setState(read())
    const update = () => setState(read())
    doc.on("update", update)
    return () => doc.off("update", update)
  }, [doc, read])
  return state
}

const HANDLES: Handle[] = ["e", "s", "se"]
const handleClass: Record<string, string> = {
  e: "right-[-5px] top-1/2 -translate-y-1/2 h-6 w-2.5 cursor-ew-resize",
  s: "bottom-[-5px] left-1/2 -translate-x-1/2 h-2.5 w-6 cursor-ns-resize",
  se: "bottom-[-5px] right-[-5px] size-3 cursor-nwse-resize",
}

export function DocsCanvas({ doc, editable, origin, onOpenInPage, comments, onOpenComments }: DocsCanvasProps) {
  const { view: surface, blocks, error } = useSurface(doc)
  const ref = React.useRef<HTMLDivElement>(null)
  const [viewport, setViewport] = React.useState<Viewport>({ x: -48, y: -48, scale: 1 })
  const [selected, setSelected] = React.useState<string | null>(null)
  const [preview, setPreview] = React.useState<{ id: string; rect: Rect } | null>(null)
  const [failure, setFailure] = React.useState<string | null>(null)
  const drag = React.useRef<Drag | null>(null)
  const pointers = React.useRef(new Map<number, { x: number; y: number }>())
  const pinch = React.useRef<number | null>(null)
  const fitted = React.useRef(false)
  const [editingNote, setEditingNote] = React.useState<{ id: string; text: string } | null>(null)
  const [deleting, setDeleting] = React.useState<string | null>(null)
  const undo = useOwnUndo(doc, origin)

  const byId = React.useMemo(() => new Map(blocks.map(b => [b.id, b])), [blocks])
  // Recomputed with the blocks: a Page edit that nests or moves a commented block updates its card.
  const threads = React.useMemo(() => blocks && comments ? threadsByBlock(doc, comments) : new Map<string, string[]>(), [doc, blocks, comments])
  const placements = React.useMemo(() => surface?.placements ?? [], [surface])
  const items = React.useMemo(() => surface?.items ?? [], [surface])
  const rects = React.useMemo(() => [...placements, ...items.filter(i => i.kind !== "connector")] as (SurfacePlacement | Exclude<SurfaceItem, { kind: "connector" }>)[], [placements, items])
  const unplaced = blocks.filter(b => !placements.some(p => p.id === b.id))

  const size = () => ({ width: ref.current?.clientWidth || 800, height: ref.current?.clientHeight || 600 })
  const local = (event: { clientX: number; clientY: number }) => {
    const box = ref.current?.getBoundingClientRect()
    return { x: event.clientX - (box?.left ?? 0), y: event.clientY - (box?.top ?? 0) }
  }
  const fit = React.useCallback(() => setViewport(fitAll(rects, size())), [rects])
  React.useEffect(() => { if (!fitted.current && surface) { fitted.current = true; setViewport(fitAll(rects, size())) } }, [surface, rects])

  const write = (action: () => void) => {
    if (!editable) return
    try { action(); setFailure(null) } catch (e) { setFailure(failureText(e)) }
  }
  const removeFromCanvas = (id: string) => { write(() => documentSurface.unplace(doc, id, origin)); setSelected(null) }
  const deleteItem = (id: string) => { write(() => documentSurface.removeItem(doc, id, origin)); setSelected(null) }
  const deleteBlock = (id: string) => { write(() => documentSurface.deleteBlock(doc, id, origin)); setSelected(null); setDeleting(null) }
  const saveNote = () => {
    const note = editingNote
    setEditingNote(null)
    if (!note) return
    const current = items.find(i => i.id === note.id)
    if (current && current.kind !== "connector" && (current.text ?? "") !== note.text) write(() => documentSurface.setText(doc, note.id, note.text, origin))
  }
  const rectOf = (id: string): Rect | null => {
    const found = rects.find(r => r.id === id)
    return found ? { x: found.x, y: found.y, w: found.w, h: found.h } : null
  }
  const commit = (id: string, rect: Rect) => write(() => documentSurface.move(doc, id, rect, origin))

  // Wheel: Ctrl/⌘ (and trackpad pinch, which browsers report with ctrlKey) zooms at the pointer; otherwise pan.
  React.useEffect(() => {
    const element = ref.current
    if (!element) return
    const onWheel = (event: WheelEvent) => {
      event.preventDefault()
      const box = element.getBoundingClientRect()
      const point = { x: event.clientX - box.left, y: event.clientY - box.top }
      setViewport(v => event.ctrlKey || event.metaKey ? zoomAt(v, point, wheelFactor(event.deltaY, event.deltaMode)) : pan(v, -event.deltaX, -event.deltaY))
    }
    element.addEventListener("wheel", onWheel, { passive: false })
    return () => element.removeEventListener("wheel", onWheel)
  }, [])

  const cancel = () => {
    drag.current = null; pinch.current = null; setPreview(null)
  }

  const onPointerDown = (event: React.PointerEvent, target?: { id: string; handle?: Handle }) => {
    pointers.current.set(event.pointerId, local(event))
    if (pointers.current.size === 2) {
      // Two touches: pinch zoom around their midpoint; any card drag is abandoned.
      const [a, b] = [...pointers.current.values()]
      pinch.current = Math.hypot(a.x - b.x, a.y - b.y); drag.current = null; setPreview(null)
      return
    }
    event.stopPropagation()
    ;(event.currentTarget as Element).setPointerCapture?.(event.pointerId)
    const start = target ? rectOf(target.id) : null
    if (target) setSelected(target.id)
    else setSelected(null)
    const primary = event.button === 0
    const movable = target && start && editable && primary && !(items.find(i => i.id === target.id)?.kind === "connector")
    drag.current = movable
      ? { kind: target.handle ? "resize" : "move", pointer: event.pointerId, id: target.id, x: event.clientX, y: event.clientY, start: start!, handle: target.handle }
      : { kind: "pan", pointer: event.pointerId, x: event.clientX, y: event.clientY }
  }

  const onPointerMove = (event: React.PointerEvent) => {
    if (pointers.current.has(event.pointerId)) pointers.current.set(event.pointerId, local(event))
    if (pinch.current !== null && pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()]
      const distance = Math.hypot(a.x - b.x, a.y - b.y)
      if (pinch.current > 0) { const ratio = distance / pinch.current; setViewport(v => zoomAt(v, { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }, ratio)) }
      pinch.current = distance
      return
    }
    const d = drag.current
    if (!d || d.pointer !== event.pointerId) return
    const dx = event.clientX - d.x, dy = event.clientY - d.y
    if (d.kind === "pan") { d.x = event.clientX; d.y = event.clientY; setViewport(v => pan(v, dx, dy)); return }
    setPreview({ id: d.id, rect: d.kind === "move" ? moved(d.start, dx, dy, viewport.scale) : resized(d.start, d.handle!, dx, dy, viewport.scale) })
  }

  const onPointerUp = (event: React.PointerEvent) => {
    pointers.current.delete(event.pointerId)
    if (pointers.current.size < 2) pinch.current = null
    const d = drag.current
    if (!d || d.pointer !== event.pointerId) return
    drag.current = null
    // One write per completed gesture: history and retained values stay bounded.
    if (d.kind !== "pan" && preview && preview.id === d.id) {
      const { x, y, w, h } = preview.rect, s = d.start
      if (x !== s.x || y !== s.y || w !== s.w || h !== s.h) commit(d.id, preview.rect)
    }
    setPreview(null)
  }

  const onPointerCancel = (event: React.PointerEvent) => { pointers.current.delete(event.pointerId); cancel() }

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (editingNote) return
    if (editable && (event.ctrlKey || event.metaKey) && (event.key === "z" || event.key === "Z" || event.key === "y")) {
      event.preventDefault()
      if (event.key === "y" || event.shiftKey) undo.redo(); else undo.undo()
      return
    }
    if (event.key === "Escape") { if (drag.current) cancel(); else setSelected(null); return }
    const center = { x: size().width / 2, y: size().height / 2 }
    if (event.target === ref.current || !selected) {
      if (event.key === "+" || event.key === "=") { event.preventDefault(); setViewport(v => zoomAt(v, center, 1.2)) }
      else if (event.key === "-") { event.preventDefault(); setViewport(v => zoomAt(v, center, 1 / 1.2)) }
      else if (event.key === "0" || (event.key === "!" && event.shiftKey)) { event.preventDefault(); fit() }
      return
    }
    const rect = rectOf(selected)
    if (!rect) return
    if (event.key === "Enter") {
      event.preventDefault()
      if (byId.has(selected)) onOpenInPage?.(selected)
      else if (editable) { const note = items.find(i => i.id === selected); if (note && note.kind === "note") setEditingNote({ id: note.id, text: note.text ?? "" }) }
      return
    }
    if (event.key === "f" || event.key === "F") { event.preventDefault(); setViewport(focusRect(rect, size())); return }
    if (!editable) return
    const next = nudged(rect, event.key, event.altKey)
    if (next) { event.preventDefault(); commit(selected, next); return }
    if (event.key === "Delete" || event.key === "Backspace") {
      event.preventDefault()
      // A placement is removed from the canvas only; the block stays in the Page.
      if (placements.some(p => p.id === selected)) removeFromCanvas(selected)
      else deleteItem(selected)
    }
  }

  const topZ = () => Math.max(0, ...placements.map(p => p.z), ...items.map(i => i.z)) + 1
  const addBlock = (id: string) => write(() => {
    const at = nextPlacement(rects)
    documentSurface.place(doc, id, { ...at, z: topZ() }, origin)
    setSelected(id); setViewport(focusRect(at, size()))
  })
  const addNote = () => write(() => {
    const center = toWorld(viewport, { x: size().width / 2 - 100, y: size().height / 2 - 80 })
    const id = "note-" + crypto.randomUUID()
    documentSurface.putItem(doc, id, { kind: "note", x: Math.round(center.x), y: Math.round(center.y), w: 200, h: 160, z: topZ(), text: "", tone: "yellow" }, origin)
    setSelected(id)
  })

  const selectedEntry: { kind: "block"; id: string } | { kind: "note"; id: string; text: string } | null = !selected ? null
    : placements.some(p => p.id === selected) && byId.has(selected) ? { kind: "block", id: selected }
    : (() => { const note = items.find(i => i.id === selected); return note && note.kind === "note" ? { kind: "note" as const, id: note.id, text: note.text ?? "" } : null })()
  const deletingBlock = deleting ? byId.get(deleting) : undefined
  const deletingThreads = deleting ? threads.get(deleting)?.length ?? 0 : 0
  const deletingConnectors = deleting ? items.filter(i => i.kind === "connector" && (i.from.ref === deleting || i.to.ref === deleting)).length : 0

  if (error) return <div role="alert" className="m-6 rounded-md border border-destructive/40 p-4 text-sm">This canvas contains data this version cannot read. Nothing was changed; reload to update.</div>

  const shown = (r: { id: string; x: number; y: number; w: number; h: number }) => preview?.id === r.id ? { ...r, ...preview.rect } : r
  const centre = (id: string) => { const r = rects.find(x => x.id === id); if (!r) return null; const s = shown(r); return { x: s.x + s.w / 2, y: s.y + s.h / 2 } }
  const zoomBy = (factor: number) => setViewport(v => zoomAt(v, { x: size().width / 2, y: size().height / 2 }, factor))

  return <div className="relative h-full min-h-[480px] w-full overflow-hidden bg-muted/30" data-docs-canvas data-editable={editable ? "true" : "false"}>
    <div ref={ref} role="application" aria-roledescription="canvas" aria-label="Canvas" tabIndex={0}
      className="absolute inset-0 touch-none select-none outline-none focus-visible:ring-2 focus-visible:ring-ring"
      onPointerDown={e => onPointerDown(e)} onPointerMove={onPointerMove} onPointerUp={onPointerUp} onPointerCancel={onPointerCancel}
      onContextMenu={e => e.preventDefault()} onKeyDown={onKeyDown}>
      <div data-canvas-world className="absolute left-0 top-0 origin-top-left"
        style={{ transform: `scale(${viewport.scale}) translate(${-viewport.x}px, ${-viewport.y}px)` }}>
        <svg aria-hidden className="pointer-events-none absolute left-0 top-0 overflow-visible" width="1" height="1">
          {items.filter((i): i is Extract<SurfaceItem, { kind: "connector" }> => i.kind === "connector").map(c => {
            const a = centre(c.from.ref), b = centre(c.to.ref)
            if (!a || !b) return null
            return <line key={c.id} data-connector={c.id} x1={a.x} y1={a.y} x2={b.x} y2={b.y} className="stroke-muted-foreground" strokeWidth={2 / viewport.scale} />
          })}
        </svg>
        {placements.map(p => {
          const block = byId.get(p.id), r = shown(p)
          if (!block) return null
          return <div key={p.id} role="group" aria-label={block.text || "Empty block"} data-placement={p.id} data-selected={selected === p.id ? "true" : undefined}
            tabIndex={0} onFocus={() => setSelected(p.id)}
            onPointerDown={e => onPointerDown(e, { id: p.id })} onDoubleClick={() => onOpenInPage?.(p.id)}
            className={cn("absolute overflow-hidden rounded-md border bg-card p-3 text-sm shadow-sm outline-none", editable ? "cursor-grab" : "cursor-default",
              selected === p.id && "ring-2 ring-primary")}
            style={{ left: r.x, top: r.y, width: r.w, minHeight: r.h, zIndex: p.z }}>
            <p className={cn("whitespace-pre-wrap break-words", block.type === "heading" && "text-base font-semibold")}>{block.text ? <FormattedRuns runs={block.runs} /> : <span className="text-muted-foreground">Empty block</span>}</p>
            {(threads.get(p.id)?.length ?? 0) > 0 && <button type="button" data-card-comments={p.id}
              aria-label={`${threads.get(p.id)!.length} open ${threads.get(p.id)!.length === 1 ? "comment" : "comments"} on this block`}
              onPointerDown={e => e.stopPropagation()} onClick={() => onOpenComments?.(p.id, threads.get(p.id)!)}
              className="absolute right-1.5 top-1.5 inline-flex items-center gap-1 rounded-sm bg-warn/20 px-1.5 py-0.5 text-xs tabular-nums hover:bg-warn/35 coarse:min-h-[44px]">
              <MessageSquare aria-hidden className="size-3.5" />{threads.get(p.id)!.length}
            </button>}
            {editable && selected === p.id && HANDLES.map(h => <span key={h} aria-hidden data-handle={h}
              onPointerDown={e => onPointerDown(e, { id: p.id, handle: h })}
              className={cn("absolute rounded-sm border border-primary bg-background", handleClass[h])} />)}
          </div>
        })}
        {items.filter(i => i.kind !== "connector").map(i => {
          const item = i as Exclude<SurfaceItem, { kind: "connector" }>, r = shown(item)
          return <div key={item.id} role="group" aria-label={item.kind === "note" ? `Note: ${item.text || "empty"}` : `Shape: ${item.text ?? item.shape}`}
            data-item={item.id} tabIndex={0} onFocus={() => setSelected(item.id)} onPointerDown={e => { if (editingNote?.id !== item.id) onPointerDown(e, { id: item.id }) }}
            onDoubleClick={() => { if (editable && item.kind === "note") setEditingNote({ id: item.id, text: item.text ?? "" }) }}
            className={cn("absolute rounded-md border p-2 text-sm outline-none", NOTE_TONES[item.tone] ?? "bg-card", item.kind === "shape" && item.shape === "ellipse" && "rounded-full",
              selected === item.id && "ring-2 ring-primary")}
            style={{ left: r.x, top: r.y, width: r.w, height: r.h, zIndex: item.z }}>
            {editingNote?.id === item.id
              ? <textarea autoFocus aria-label="Note text" value={editingNote.text} maxLength={documentSurface.LIMITS.text}
                onChange={e => setEditingNote({ id: item.id, text: e.target.value })} onBlur={saveNote}
                onPointerDown={e => e.stopPropagation()}
                onKeyDown={e => { e.stopPropagation(); if (e.key === "Escape") setEditingNote(null); else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) saveNote() }}
                className="size-full resize-none bg-transparent outline-none" />
              : <p className="whitespace-pre-wrap break-words">{item.text || (editable ? <span className="text-muted-foreground">Double-click to write</span> : null)}</p>}
          </div>
        })}
      </div>
    </div>

    {placements.length === 0 && items.length === 0 && <p className="pointer-events-none absolute inset-x-0 top-1/3 text-center text-sm text-muted-foreground">
      {editable ? "Nothing is on the canvas yet. Add blocks from this document below." : "Nothing is on the canvas yet."}
    </p>}

    {editable && unplaced.length > 0 && <details className="absolute left-3 top-3 max-h-[50%] w-64 overflow-y-auto rounded-md border bg-background text-sm shadow-sm">
      <summary className="cursor-pointer px-3 py-2 font-medium coarse:min-h-[44px]">Add blocks ({unplaced.length})</summary>
      <ul className="grid gap-px p-1">
        {unplaced.map(b => <li key={b.id}><button type="button" onClick={() => addBlock(b.id)}
          className="w-full truncate rounded-sm px-2 py-1.5 text-left hover:bg-accent coarse:min-h-[44px]">{b.text || "Empty block"}</button></li>)}
      </ul>
    </details>}

    {selectedEntry && <div role="toolbar" aria-label="Selection" data-canvas-selection
      className="absolute left-1/2 top-3 flex -translate-x-1/2 items-center gap-1 rounded-md border bg-background p-1 text-sm shadow-sm">
      {selectedEntry.kind === "block" && <>
        <Button type="button" size="sm" variant="ghost" onClick={() => onOpenInPage?.(selectedEntry.id)}><FileText aria-hidden className="size-4" />Open in Page</Button>
        {editable && <Button type="button" size="sm" variant="ghost" onClick={() => removeFromCanvas(selectedEntry.id)}><X aria-hidden className="size-4" />Remove from canvas</Button>}
        {editable && <Button type="button" size="sm" variant="ghost" className="text-destructive" onClick={() => setDeleting(selectedEntry.id)}><Trash2 aria-hidden className="size-4" />Delete block…</Button>}
      </>}
      {selectedEntry.kind === "note" && editable && <>
        <Button type="button" size="sm" variant="ghost" onClick={() => setEditingNote({ id: selectedEntry.id, text: selectedEntry.text })}><Pencil aria-hidden className="size-4" />Edit text</Button>
        <Button type="button" size="sm" variant="ghost" className="text-destructive" onClick={() => deleteItem(selectedEntry.id)}><Trash2 aria-hidden className="size-4" />Delete note</Button>
      </>}
    </div>}

    <div className="absolute bottom-3 right-3 flex items-center gap-1 rounded-md border bg-background p-1 shadow-sm">
      {editable && <Button type="button" size="sm" variant="ghost" onClick={() => undo.undo()} disabled={!undo.canUndo} aria-label="Undo my last canvas change"><Undo2 aria-hidden className="size-4" /></Button>}
      {editable && <Button type="button" size="sm" variant="ghost" onClick={() => undo.redo()} disabled={!undo.canRedo} aria-label="Redo"><Redo2 aria-hidden className="size-4" /></Button>}
      {editable && <Button type="button" size="sm" variant="ghost" onClick={addNote} aria-label="Add note"><StickyNote aria-hidden className="size-4" /></Button>}
      <Button type="button" size="sm" variant="ghost" onClick={() => zoomBy(1 / 1.2)} aria-label="Zoom out"><Minus aria-hidden className="size-4" /></Button>
      <span className="w-12 text-center font-mono text-xs tabular-nums" aria-live="polite" data-zoom>{Math.round(viewport.scale * 100)}%</span>
      <Button type="button" size="sm" variant="ghost" onClick={() => zoomBy(1.2)} aria-label="Zoom in"><Plus aria-hidden className="size-4" /></Button>
      <Button type="button" size="sm" variant="ghost" onClick={fit} aria-label="Fit to screen"><Maximize aria-hidden className="size-4" /></Button>
    </div>
    {failure && <p role="alert" className="absolute bottom-14 right-3 rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm">{failure}</p>}

    <ConfirmDialog open={deleting !== null} onOpenChange={open => { if (!open) setDeleting(null) }} destructive
      title="Delete this block?" description={deletingBlock ? `“${deletingBlock.text || "Empty block"}” is part of the Page as well as the Canvas.` : undefined}
      consequences={deleting ? deleteConsequences(deletingThreads, deletingConnectors) : []}
      confirmLabel="Delete block" onConfirm={() => { if (deleting) deleteBlock(deleting) }} />
  </div>
}

function failureText(cause: unknown): string {
  const message = cause instanceof Error ? cause.message : ""
  if (message === "a document keeps at least one block") return "A document keeps at least one block, so this one cannot be deleted."
  if (message === "surface history limit" || message === "surface entry limit") return "The canvas is full. This change was not made."
  return message || "This change could not be made."
}

/** What deleting a block does, for its confirmation (R71 §0.4/2 and /4). */
export function deleteConsequences(threads: number, connectors: number): { tone: "lost" | "kept" | "warn"; text: string }[] {
  return [
    { tone: "lost", text: "The block is deleted from the Page and from the Canvas." },
    ...(threads > 0 ? [{ tone: "warn" as const, text: `${threads} open comment ${threads === 1 ? "thread becomes" : "threads become"} detached; ${threads === 1 ? "it keeps" : "they keep"} the quoted text.` }] : []),
    ...(connectors > 0 ? [{ tone: "warn" as const, text: `${connectors} ${connectors === 1 ? "connector loses" : "connectors lose"} an end.` }] : []),
    { tone: "kept", text: "History keeps it: undo here, or restore a version." },
  ]
}

/**
 * This person's own undo on the Canvas: only changes made with their origin,
 * across the Page and the surface roots, so a deleted block comes back with its
 * placement and nobody else's work is ever taken back (R71 §0.4/5).
 */
function useOwnUndo(doc: Y.Doc, origin: string) {
  const [state, setState] = React.useState({ canUndo: false, canRedo: false })
  // The manager lives and dies with the effect, so a StrictMode remount builds a fresh one instead of reusing a destroyed one.
  const manager = React.useRef<Y.UndoManager | null>(null)
  React.useEffect(() => {
    const own = new Y.UndoManager([doc.getXmlFragment("default"), ...documentSurface.ROOTS.map(name => doc.getMap(name))], { trackedOrigins: new Set([origin]), captureTimeout: 0 })
    manager.current = own
    const sync = () => setState({ canUndo: own.canUndo(), canRedo: own.canRedo() })
    own.on("stack-item-added", sync); own.on("stack-item-popped", sync); own.on("stack-cleared", sync)
    sync()
    return () => { own.off("stack-item-added", sync); own.off("stack-item-popped", sync); own.off("stack-cleared", sync); own.destroy(); if (manager.current === own) manager.current = null }
  }, [doc, origin])
  return { ...state, undo: () => { manager.current?.undo() }, redo: () => { manager.current?.redo() } }
}
