"use client"

import { useEffect, useRef, useState, type KeyboardEvent } from "react"
import { ArrowLeft, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { designLinkTarget, designScreenOf, type DesignNode } from "./design-document"
import { DesignNodeShape } from "./design-node-shape"

/**
 * Local prototype playback (#4449). Navigation is component state only: it
 * never receives the Y.Doc, so playing cannot mutate the shared document.
 * Nodes may change live underneath; a screen or target that disappears is
 * shown as such instead of throwing.
 */
export function DesignPrototypePlayer({ nodes, startScreenId, onClose }: { nodes: DesignNode[]; startScreenId: string; onClose: () => void }) {
 const [trail, setTrail] = useState<string[]>([startScreenId])
 const [hint, setHint] = useState("")
 const dialog = useRef<HTMLDivElement>(null)
 const heading = useRef<HTMLHeadingElement>(null)
 const currentId = trail[trail.length - 1]
 const current = nodes.find(node => node.id === currentId && node.type === "frame")
 useEffect(() => { heading.current?.focus() }, [currentId])
 const byId = new Map(nodes.map(node => [node.id, node]))
 const position = (node: DesignNode, seen = new Set<string>()): { x: number; y: number } => {
  if (seen.has(node.id)) return { x: Number(node.x) || 0, y: Number(node.y) || 0 }
  seen.add(node.id)
  const parent = node.parentId ? byId.get(node.parentId) : undefined
  const offset = parent ? position(parent, seen) : { x: 0, y: 0 }
  return { x: offset.x + (Number(node.x) || 0), y: offset.y + (Number(node.y) || 0) }
 }
 const origin = current ? position(current) : { x: 0, y: 0 }
 const layers = current ? nodes.filter(node => node.type !== "frame" && designScreenOf(nodes, node) === current.id) : []
 const width = Math.max(1, Number(current?.width) || 1), height = Math.max(1, Number(current?.height) || 1)
 const back = () => { setHint(""); setTrail(previous => previous.length > 1 ? previous.slice(0, -1) : previous) }
 const follow = (node: DesignNode) => {
  const link = designLinkTarget(nodes, node)
  if (link.kind === "screen") { setHint(""); setTrail(previous => [...previous, link.screen.id]) }
  else if (link.kind === "missing") setHint(`${node.name || node.text || "This layer"}: linked screen was deleted. Ask an editor to choose a new target.`)
 }
 const keyDown = (event: KeyboardEvent<HTMLDivElement>) => {
  if (event.key === "Escape") { event.stopPropagation(); onClose() }
  else if (event.key === "Tab") {
   // Keep focus inside the modal player.
   const focusable = [...(dialog.current?.querySelectorAll<HTMLElement>("button:not([disabled]), [tabindex='0']") ?? [])]
   if (!focusable.length) return
   const first = focusable[0], last = focusable[focusable.length - 1]
   if (event.shiftKey && (document.activeElement === first || document.activeElement === heading.current)) { event.preventDefault(); last.focus() }
   else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
  }
 }
 return <div ref={dialog} role="dialog" aria-modal="true" aria-label="Prototype playback" onKeyDown={keyDown} className="fixed inset-0 z-50 flex flex-col bg-background/95 text-foreground">
  <header className="flex items-center gap-2 border-b px-4 py-3">
   <Button variant="outline" size="sm" disabled={trail.length < 2} onClick={back}><ArrowLeft className="size-4" />Back</Button>
   <h2 ref={heading} tabIndex={-1} className="mr-auto truncate text-sm font-semibold outline-none">{current ? current.name || "Screen" : "Screen unavailable"}</h2>
   <span className="hidden text-xs text-muted-foreground sm:inline">Prototype · nothing you click here changes the Design</span>
   <Button variant="ghost" size="sm" onClick={onClose}><X className="size-4" />Exit<span className="sr-only"> prototype (Esc)</span></Button>
  </header>
  <p role="status" className="min-h-6 px-4 py-1 text-xs text-muted-foreground">{current ? hint : "This screen was deleted while you were playing. Go back or exit."}</p>
  <div className="flex min-h-0 flex-1 items-start justify-center overflow-auto p-4">
   {current && <svg role="img" aria-label={`${current.name || "Screen"} prototype`} width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="max-w-full shrink-0 shadow-lg">
    <DesignNodeShape node={current} width={width} height={height} />
    {layers.map(node => {
     const { x, y } = position(node)
     const w = Math.max(1, Number(node.width) || 1), h = Math.max(1, Number(node.height) || 1)
     const link = designLinkTarget(nodes, node)
     const shape = <DesignNodeShape node={node} width={w} height={h} />
     const transform = `translate(${x - origin.x},${y - origin.y})`
     if (link.kind === "none") return <g key={node.id} transform={transform}>{shape}</g>
     const label = `${node.name || node.text || node.type}: ${link.kind === "screen" ? `open ${link.screen.name || "Screen"}` : "linked screen was deleted"}`
     return <g key={node.id} transform={transform} role="button" tabIndex={0} aria-label={label} aria-disabled={link.kind === "missing" ? "true" : undefined} data-prototype-hotspot={node.id} className="group outline-none" style={{ cursor: link.kind === "screen" ? "pointer" : "not-allowed" }} onClick={() => follow(node)} onKeyDown={event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); follow(node) } }}>
      {shape}
      <rect width={w} height={h} fill="none" rx="6" strokeWidth="2" strokeDasharray={link.kind === "missing" ? "4 3" : undefined} className={link.kind === "missing" ? "stroke-warn" : "stroke-transparent group-hover:stroke-primary group-focus-visible:stroke-primary"} pointerEvents="none" />
     </g>
    })}
   </svg>}
  </div>
 </div>
}
