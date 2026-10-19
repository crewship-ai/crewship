"use client"

import * as React from "react"
import type { SlideDeck, SlideElement } from "@/lib/slides/model"
import { useIsMobile } from "@/hooks/use-mobile"
import { addElement, canReorder, nudgeElements, removeElements, reorderElements, resizeElement, type Layer, type Rect } from "./element-edit"
import { insertSlide, isNativeSlideDeck, moveSlide, orderedSlides } from "./native-model"
import { SlideCanvas } from "./slide-canvas"
import { SlidePresent } from "./slide-present"

const control = "min-h-9 coarse:min-h-12 rounded border px-3 disabled:opacity-50"
const fontProperties = (element: SlideElement) => element.type === "text" ? element.text : element.type === "table" ? element.table : undefined
const fillProperties = (element: SlideElement) => element.type === "shape" ? element.shape : element.type === "table" ? element.table : undefined
const input = "min-h-9 coarse:min-h-12 w-full rounded border bg-background px-2"

export interface SlidesEditorProps {
  deck: unknown
  /** Emits an unsaved candidate only. Persistence must be separately acknowledged. */
  onChange?: (candidate: SlideDeck) => void
  readOnly?: boolean
  disabled?: boolean
  saveDisabled?: boolean
  /** Requests persistence only; the caller owns authority and acknowledgement. */
  onSave?: (candidate: SlideDeck) => void
  persistenceStatus?: string
}

export function SlidesEditor({ deck, onChange, readOnly, disabled, saveDisabled, onSave, persistenceStatus }: SlidesEditorProps) {
  if (!isNativeSlideDeck(deck)) return <p role="alert">This Slides format is unavailable. Its content cannot be edited.</p>
  return <NativeEditor deck={deck} onChange={onChange} readOnly={Boolean(readOnly || disabled || !onChange)} onSave={onSave} saveDisabled={saveDisabled} persistenceStatus={persistenceStatus} />
}

const layers: { layer: Layer; label: string }[] = [{ layer: "front", label: "Bring to front" }, { layer: "forward", label: "Bring forward" }, { layer: "backward", label: "Send backward" }, { layer: "back", label: "Send to back" }]
const typing = (target: EventTarget) => target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName))
const HISTORY_LIMIT = 100

function NativeEditor({ deck, onChange, readOnly: parentReadOnly, onSave, saveDisabled, persistenceStatus }: SlidesEditorProps & { deck: SlideDeck; readOnly: boolean }) {
  const phone = useIsMobile()
  const readOnly = parentReadOnly || phone
  const [slideId, setSlideId] = React.useState(deck.slide_order[0])
  const [selection, setSelection] = React.useState<string[]>([])
  const [present, setPresent] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const [overflow, setOverflow] = React.useState<string[]>([])
  const [history, setHistory] = React.useState<{ past: SlideDeck[]; future: SlideDeck[] }>({ past: [], future: [] })
  const emitted = React.useRef<string>(undefined)
  const lastKey = React.useRef<string>(undefined)
  const slide = deck.slides.find(value => value.id === slideId)
  const ids = selection.filter(id => slide?.elements.some(element => element.id === id))
  const elementId = ids.length === 1 ? ids[0] : undefined
  const element = slide?.elements.find(value => value.id === elementId)
  // A deck we did not emit (reload after a conflict, another save) ends local undo history.
  React.useEffect(() => {
    if (emitted.current !== undefined && JSON.stringify(deck) !== emitted.current) { setHistory({ past: [], future: [] }); lastKey.current = undefined; emitted.current = undefined }
  }, [deck])
  const emit = (candidate: SlideDeck) => { emitted.current = JSON.stringify(candidate); onChange?.(candidate) }
  /** `coalesce` merges consecutive edits of one field into a single undo step. */
  const changed = (candidate: SlideDeck, coalesce?: string) => {
    if (readOnly) return false
    if (!isNativeSlideDeck(candidate)) { setError("This change exceeds the slide format limits. The previous draft is retained."); return false }
    setError(undefined)
    if (!coalesce || lastKey.current !== coalesce) setHistory(value => ({ past: [...value.past, deck].slice(-HISTORY_LIMIT), future: [] }))
    else setHistory(value => ({ ...value, future: [] }))
    lastKey.current = coalesce
    emit(candidate); return true
  }
  const travel = (direction: "undo" | "redo") => {
    if (readOnly) return
    const from = direction === "undo" ? history.past : history.future, target = from.at(-1)
    if (!target) return
    const rest = from.slice(0, -1)
    setHistory(direction === "undo" ? { past: rest, future: [...history.future, deck] } : { past: [...history.past, deck], future: rest })
    lastKey.current = undefined; setError(undefined); emit(target)
    if (!target.slides.some(value => value.id === slideId)) setSlideId(target.slide_order[0])
  }
  const editSlide = (edit: (value: SlideDeck["slides"][number]) => void, coalesce?: string) => {
    const candidate = structuredClone(deck), target = candidate.slides.find(value => value.id === slideId)
    if (!target) return
    edit(target); changed(candidate, coalesce)
  }
  const editElement = (edit: (value: SlideElement) => void, field = "inspector") => editSlide(value => {
    const target = value.elements.find(item => item.id === elementId)
    if (target) edit(target)
  }, `${elementId}:${field}`)
  const pick = (id: string, additive: boolean, keepGroup = false) => setSelection(value => additive ? (value.includes(id) ? value.filter(item => item !== id) : [...value, id]) : keepGroup && value.includes(id) ? value : [id])
  const nudge = (dx: number, dy: number) => { if (ids.length) changed(nudgeElements(deck, slideId, ids, dx, dy)) }
  const remove = () => { if (ids.length && changed(removeElements(deck, slideId, ids))) setSelection([]) }
  const onKeyDown = (event: React.KeyboardEvent) => {
    if (typing(event.target) || (event.target as HTMLElement).closest("[role=dialog]")) return
    const mod = event.ctrlKey || event.metaKey, key = event.key.toLowerCase()
    if (mod && (key === "z" || key === "y")) { event.preventDefault(); travel(key === "y" || event.shiftKey ? "redo" : "undo"); return }
    if (readOnly) return
    const step = event.shiftKey ? 10 : 1, arrows: Record<string, [number, number]> = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] }
    if (event.key in arrows && ids.length) {
      event.preventDefault()
      const [dx, dy] = arrows[event.key]
      if (event.altKey && element) changed(resizeElement(deck, slideId, element.id, { x: element.x, y: element.y, width: element.width + dx, height: element.height + dy }))
      else nudge(dx, dy)
    } else if ((event.key === "Delete" || event.key === "Backspace") && ids.length) { event.preventDefault(); remove() }
    else if (event.key === "Escape" && ids.length) setSelection([])
  }
  const resize = (id: string, rect: Rect) => { changed(resizeElement(deck, slideId, id, rect)) }
  const position = deck.slide_order.indexOf(slideId)
  return <section aria-label="Slides editor" className="space-y-3" onKeyDown={onKeyDown}>
    <p role="status" className="text-sm text-muted-foreground">{persistenceStatus ?? "Changes in this preview are not saved."} Images and comments are not available.{phone && " Editing is available on a larger screen; this is a read-only preview."}</p>
    {error && <p role="alert">{error}</p>}
    <div className="flex flex-wrap gap-2" aria-label="Slide actions">
      {onSave && <button className={control} disabled={readOnly || saveDisabled} onClick={() => { if (!readOnly && !saveDisabled) onSave(deck) }}>Save changes</button>}
      <button className={control} disabled={readOnly || !slide || deck.slides.length >= 100} onClick={() => { const next = insertSlide(deck, slideId); if (changed(next.deck)) { setSlideId(next.slideId); setSelection([]) } }}>New slide</button>
      <button className={control} disabled={readOnly || !slide || deck.slides.length >= 100} onClick={() => { const next = insertSlide(deck, slideId, true); if (changed(next.deck)) { setSlideId(next.slideId); setSelection([]) } }}>Duplicate slide</button>
      <button className={control} disabled={readOnly || !slide || deck.slides.length <= 1} onClick={() => {
        const candidate = structuredClone(deck)
        candidate.slides = candidate.slides.filter(value => value.id !== slideId); candidate.slide_order = candidate.slide_order.filter(value => value !== slideId)
        if (changed(candidate)) { setSlideId(candidate.slide_order[Math.min(position, candidate.slide_order.length - 1)]); setSelection([]) }
      }}>Delete slide</button>
      <button className={control} disabled={readOnly || position <= 0} onClick={() => changed(moveSlide(deck, slideId, -1))}>Move slide earlier</button>
      <button className={control} disabled={readOnly || position < 0 || position >= deck.slides.length - 1} onClick={() => changed(moveSlide(deck, slideId, 1))}>Move slide later</button>
      <button className={control} disabled={readOnly || history.past.length === 0} onClick={() => travel("undo")}>Undo</button>
      <button className={control} disabled={readOnly || history.future.length === 0} onClick={() => travel("redo")}>Redo</button>
      <button className={control} disabled={!slide} onClick={() => setPresent(true)}>Present</button>
    </div>
    <nav aria-label="Slides" className="flex flex-wrap gap-2">{orderedSlides(deck).map((value, index) => <button key={value.id} className={control} aria-current={value.id === slideId ? "true" : undefined} onClick={() => { setSlideId(value.id); setSelection([]) }}>Slide {index + 1}</button>)}</nav>
    {slide ? <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_18rem]">
      <div className="min-w-0 space-y-3">
        <SlideCanvas deck={deck} slideId={slideId} selectedIds={ids} onOverflowChange={setOverflow}
          {...(readOnly ? {} : { onSelectElement: (id: string, additive: boolean) => pick(id, additive, true), onClearSelection: () => setSelection([]), onMoveElements: nudge, onResizeElement: resize })} />
        {slide.elements.length === 0 && <p role="status" className="text-sm">This slide is empty.{!readOnly && " Add text, a shape or a table to start."}</p>}
        {overflow.length > 0 && <p role="alert">Some text or table content is clipped by its frame ({overflow.length} element{overflow.length === 1 ? "" : "s"}). Enlarge the element or shorten its content.</p>}
        <label className="block">Speaker notes<textarea className={`${input} min-h-24`} value={slide.notes} disabled={readOnly} onChange={event => editSlide(value => { value.notes = event.target.value }, `notes:${slideId}`)} /></label>
      </div>
      <div className="space-y-3">
        <div className="flex flex-wrap gap-2">{(["text", "shape", "table"] as const).map(type => <button key={type} className={control} disabled={readOnly} onClick={() => {
          const added = addElement(deck, slideId, type)
          if (changed(added.deck)) setSelection([added.id])
        }}>Add {type}</button>)}</div>
        <div aria-label="Slide elements" className="flex flex-wrap gap-2">{slide.elements.map((value, index) => <button key={value.id} className={control} aria-pressed={value.id === elementId} onClick={event => pick(value.id, event.shiftKey)}>Select {value.type} {index + 1}</button>)}</div>
        <div role="group" aria-label="Layer order" className="flex flex-wrap gap-2">{layers.map(({ layer, label }) => <button key={layer} className={control} disabled={readOnly || !canReorder(deck, slideId, ids, layer)} onClick={() => changed(reorderElements(deck, slideId, ids, layer))}>{label}</button>)}</div>
        {element ? <fieldset disabled={readOnly} className="space-y-2"><legend>Selected {element.type}</legend>
          <div className="grid grid-cols-2 gap-2">{(["x", "y", "width", "height"] as const).map(field => <label key={field} className="block">{field}<input className={input} type="number" step={1} min={field === "width" || field === "height" ? 1 : 0} max={field === "x" || field === "width" ? deck.width : deck.height} value={element[field]} onChange={event => editElement(value => { value[field] = Number(event.target.value) })} /></label>)}</div>
          {element.type === "text" && <>
            <label className="block">Text<textarea className={`${input} min-h-24`} value={element.text.value} onChange={event => editElement(value => { if (value.type === "text") value.text.value = event.target.value })} /></label>
            <label className="block">Alignment<select className={input} value={element.text.align} onChange={event => editElement(value => { if (value.type === "text") value.text.align = event.target.value as "left" | "center" | "right" })}>{["left", "center", "right"].map(value => <option key={value}>{value}</option>)}</select></label>
          </>}
          {element.type !== "shape" && <>
            <label className="block">Font family<input className={input} value={fontProperties(element)!.font_family} onChange={event => editElement(value => { const style = fontProperties(value); if (style) style.font_family = event.target.value })} /></label>
            <label className="block">Font size<input className={input} type="number" min={1} max={160} step={1} value={fontProperties(element)!.font_size} onChange={event => editElement(value => { const style = fontProperties(value); if (style) style.font_size = Number(event.target.value) })} /></label>
            <label className="block">Text color<input className={input} type="color" value={fontProperties(element)!.color} onChange={event => editElement(value => { const style = fontProperties(value); if (style) style.color = event.target.value })} /></label>
          </>}
          {element.type === "shape" && <>
            <label className="block">Shape<select className={input} value={element.shape.kind} onChange={event => editElement(value => { if (value.type === "shape") value.shape.kind = event.target.value as "rectangle" | "ellipse" })}><option>rectangle</option><option>ellipse</option></select></label>
            <label className="block">Stroke width<input className={input} type="number" min={0} max={16} step={1} value={element.shape.stroke_width} onChange={event => editElement(value => { if (value.type === "shape") value.shape.stroke_width = Number(event.target.value) })} /></label>
          </>}
          {element.type !== "text" && <label className="block">Fill<input className={input} type="color" value={fillProperties(element)!.fill} onChange={event => editElement(value => { const style = fillProperties(value); if (style) style.fill = event.target.value })} /></label>}
          {element.type === "shape" && <label className="block">Stroke color<input className={input} type="color" value={element.shape.stroke} onChange={event => editElement(value => { if (value.type === "shape") value.shape.stroke = event.target.value })} /></label>}
          {element.type === "table" && <>
            <label className="block">Border color<input className={input} type="color" value={element.table.border} onChange={event => editElement(value => { if (value.type === "table") value.table.border = event.target.value })} /></label>
            {element.table.cells.map((row, r) => <div key={r} className="flex gap-1">{row.map((cell, c) => <label key={c} className="min-w-0 flex-1"><span className="text-xs">Cell {r + 1}, {c + 1}</span><input className={input} value={cell} onChange={event => editElement(value => { if (value.type === "table") value.table.cells[r][c] = event.target.value })} /></label>)}</div>)}
          </>}
          <button className={control} onClick={remove}>Delete element</button>
        </fieldset> : ids.length > 1
          ? <div className="space-y-2"><p className="text-sm">{ids.length} elements selected. Arrow keys move them together; Shift moves by 10.</p><button className={control} disabled={readOnly} onClick={remove}>Delete {ids.length} elements</button></div>
          : <p className="text-sm">Select an element to edit its content and position. Shift selects several.</p>}
      </div>
    </div> : <p role="alert">The selected slide is no longer available. Choose a slide from the list.</p>}
    {present && slide && <SlidePresent deck={deck} initialSlideId={slideId} onClose={() => setPresent(false)} />}
  </section>
}
