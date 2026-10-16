"use client"

import * as React from "react"
import type { SlideDeck, SlideElement } from "@/lib/slides/model"
import { insertSlide, isNativeSlideDeck, moveSlide, newElement, orderedSlides } from "./native-model"
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
  reorderDisabled?: boolean
  persistenceStatus?: string
  commentsAvailable?: boolean
}

export function SlidesEditor({ deck, onChange, readOnly, reorderDisabled, persistenceStatus, commentsAvailable }: SlidesEditorProps) {
  if (!isNativeSlideDeck(deck)) return <p role="alert">This Slides format is unavailable. Its content cannot be edited.</p>
  return <NativeEditor deck={deck} onChange={onChange} readOnly={readOnly || !onChange} reorderDisabled={reorderDisabled} persistenceStatus={persistenceStatus} commentsAvailable={commentsAvailable} />
}

function NativeEditor({ deck, onChange, readOnly, reorderDisabled, persistenceStatus, commentsAvailable }: SlidesEditorProps & { deck: SlideDeck; readOnly: boolean }) {
  const [slideId, setSlideId] = React.useState(deck.slide_order[0])
  const [elementId, setElementId] = React.useState<string>()
  const [present, setPresent] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const [overflow, setOverflow] = React.useState<string[]>([])
  const slide = deck.slides.find(value => value.id === slideId)
  const element = slide?.elements.find(value => value.id === elementId)
  const changed = (candidate: SlideDeck) => {
    if (readOnly) return false
    if (!isNativeSlideDeck(candidate)) { setError("This change exceeds the slide format limits. The previous draft is retained."); return false }
    setError(undefined); onChange?.(candidate); return true
  }
  const editSlide = (edit: (value: SlideDeck["slides"][number]) => void) => {
    const candidate = structuredClone(deck), target = candidate.slides.find(value => value.id === slideId)
    if (!target) return
    edit(target); changed(candidate)
  }
  const editElement = (edit: (value: SlideElement) => void) => editSlide(value => {
    const target = value.elements.find(item => item.id === elementId)
    if (target) edit(target)
  })
  const position = deck.slide_order.indexOf(slideId)
  return <section aria-label="Slides editor" className="space-y-3">
    <p role="status" className="text-sm text-muted-foreground">{persistenceStatus ?? "Changes in this preview are not saved."} {commentsAvailable ? "Images are not available." : "Images and comments are not available."}</p>
    {error && <p role="alert">{error}</p>}
    <div className="flex flex-wrap gap-2" aria-label="Slide actions">
      <button className={control} disabled={readOnly || !slide || deck.slides.length >= 100} onClick={() => { const next = insertSlide(deck, slideId); if (changed(next.deck)) { setSlideId(next.slideId); setElementId(undefined) } }}>New slide</button>
      <button className={control} disabled={readOnly || !slide || deck.slides.length >= 100} onClick={() => { const next = insertSlide(deck, slideId, true); if (changed(next.deck)) { setSlideId(next.slideId); setElementId(undefined) } }}>Duplicate slide</button>
      <button className={control} disabled={readOnly || !slide || deck.slides.length <= 1} onClick={() => {
        const candidate = structuredClone(deck)
        candidate.slides = candidate.slides.filter(value => value.id !== slideId); candidate.slide_order = candidate.slide_order.filter(value => value !== slideId)
        if (changed(candidate)) { setSlideId(candidate.slide_order[Math.min(position, candidate.slide_order.length - 1)]); setElementId(undefined) }
      }}>Delete slide</button>
      <button className={control} disabled={readOnly || reorderDisabled || position <= 0} onClick={() => changed(moveSlide(deck, slideId, -1))}>Move slide earlier</button>
      <button className={control} disabled={readOnly || reorderDisabled || position < 0 || position >= deck.slides.length - 1} onClick={() => changed(moveSlide(deck, slideId, 1))}>Move slide later</button>
      <button className={control} disabled={!slide} onClick={() => setPresent(true)}>Present</button>
    </div>
    <nav aria-label="Slides" className="flex flex-wrap gap-2">{orderedSlides(deck).map((value, index) => <button key={value.id} className={control} aria-current={value.id === slideId ? "true" : undefined} onClick={() => { setSlideId(value.id); setElementId(undefined) }}>Slide {index + 1}</button>)}</nav>
    {slide ? <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_18rem]">
      <div className="min-w-0 space-y-3">
        <SlideCanvas deck={deck} slideId={slideId} selectedElementId={elementId} onSelectElement={setElementId} onOverflowChange={setOverflow} />
        {overflow.length > 0 && <p role="alert">Some text or table content is clipped by its frame ({overflow.length} element{overflow.length === 1 ? "" : "s"}). Enlarge the element or shorten its content.</p>}
        <label className="block">Speaker notes<textarea className={`${input} min-h-24`} value={slide.notes} disabled={readOnly} onChange={event => editSlide(value => { value.notes = event.target.value })} /></label>
      </div>
      <div className="space-y-3">
        <div className="flex flex-wrap gap-2">{(["text", "shape", "table"] as const).map(type => <button key={type} className={control} disabled={readOnly} onClick={() => {
          const candidate = structuredClone(deck), value = newElement(candidate, type)
          candidate.slides.find(item => item.id === slideId)!.elements.push(value)
          if (changed(candidate)) setElementId(value.id)
        }}>Add {type}</button>)}</div>
        <div aria-label="Slide elements" className="flex flex-wrap gap-2">{slide.elements.map((value, index) => <button key={value.id} className={control} aria-pressed={value.id === elementId} onClick={() => setElementId(value.id)}>Select {value.type} {index + 1}</button>)}</div>
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
          <button className={control} onClick={() => { editSlide(value => { value.elements = value.elements.filter(item => item.id !== elementId) }); setElementId(undefined) }}>Delete element</button>
        </fieldset> : <p className="text-sm">Select an element to edit its content and position.</p>}
      </div>
    </div> : <p role="alert">The selected slide is no longer available. Choose a slide from the list.</p>}
    {present && slide && <SlidePresent deck={deck} initialSlideId={slideId} onClose={() => setPresent(false)} />}
  </section>
}
