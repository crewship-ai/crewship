"use client"

import * as React from "react"
import { ChevronDown, Download, Film, LayoutGrid, MousePointerClick, NotebookText, Play, Presentation, Square, ZoomIn, ZoomOut } from "lucide-react"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import { Textarea } from "@/components/ui/textarea"
import { useSessionSafe } from "@/hooks/use-auth"
import { useArtifactAgentDispatch } from "@/hooks/use-artifact-agent-dispatch"
import { artifactDraftKey, artifactDraftRevision, clearArtifactDraft, loadArtifactDraft, writeArtifactDraft } from "@/lib/artifact-drafts"
import { artifactSlidesMode, artifactSlideThumbnail, type ArtifactSlidesProjection } from "@/lib/pages/artifact-slides"
import type { Slide } from "@/lib/slides/model"
import { cn } from "@/lib/utils"
import { SlideCanvas } from "@/components/features/slides/slide-canvas"
import { SlidePresent } from "@/components/features/slides/slide-present"
import { useArtifactProjectMutations, useArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/use-artifact-project-data"
import { useArtifactSlides } from "@/components/features/chat/artifact-context/projects/use-artifact-slides"
import type { ArtifactComment, ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { commentQueueChip } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import "@/components/features/chat/artifact-context/viewer/artifact-slides-preview.css"
import { ArtifactAgentMode, ArtifactDrawerSection, ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame } from "../../frame"
import { enterFullscreen, exitFullscreen } from "../canvas-fullscreen"
import type { KindFrameReport } from "../design/design-detail"
import { slidesFrameStatus } from "./slides-status"

const ZOOM_STEPS = [0.1, 0.25, 0.33, 0.5, 0.75, 1, 1.25, 1.5, 2]

/** A slide's name in pickers: its first text, else its number. */
export function slideLabel(slide: Slide | undefined, index: number): string {
  const text = slide?.elements.find(element => element.type === "text" && element.text.value.trim())
  const value = text?.type === "text" ? text.text.value.trim().split("\n")[0] : ""
  return value ? (value.length > 48 ? `${value.slice(0, 47)}…` : value) : `Slide ${index + 1}`
}

/**
 * A deck inside the ArtifactFrame (Harbor 2 v87 `slides`): the slide on a
 * stage with the filmstrip under it, and the frame's slots for picking a
 * slide (view), Download and Present (output), zoom (viewport) and notes,
 * grid, filmstrip and n / N (nav). Comments and History are drawer tabs over
 * the existing artifact routes; nothing here writes the deck.
 */
export function SlidesBody({ workspaceId, slug, version, onFrame }: { workspaceId: string; slug: string; version?: number; onFrame: (report: KindFrameReport) => void }) {
  const [viewing, setViewing] = React.useState<number | undefined>(version)
  const snapshot = useArtifactProjectSnapshot(workspaceId, slug, viewing)
  const data = snapshot.data
  const status = data ? slidesFrameStatus({ ready: true, canWrite: data.can_write === true, shown: data.manifest.version, latest: data.latest_version, refreshFailed: Boolean(snapshot.error) }) : null
  const canEdit = Boolean(data && !snapshot.error && data.can_write === true)
  const key = JSON.stringify([status, canEdit])
  const report = React.useRef(onFrame); report.current = onFrame
  // eslint-disable-next-line react-hooks/exhaustive-deps -- the key holds everything reported
  React.useEffect(() => { report.current({ canEdit, status }) }, [key])
  const errorStatus = (snapshot.error as { status?: number } | null)?.status
  if (snapshot.error && (errorStatus === 403 || errorStatus === 404)) return <SlidesMessage alert text={errorStatus === 403 ? "You can't open this deck. Ask the owner for access." : "This deck isn't available. It was archived, or the link points to a private version."} />
  if (!data) {
    if (snapshot.error) return <SlidesMessage alert text="This deck could not be loaded." action={<Button type="button" size="sm" variant="outline" onClick={() => void snapshot.refetch()}>Try again</Button>} />
    return <div aria-busy="true" aria-label="Loading slides" className="flex flex-1 flex-col gap-3 p-6 pt-16"><Skeleton className="mx-auto aspect-video w-full max-w-4xl" /></div>
  }
  const mode = artifactSlidesMode(data.artifact_kind, data.manifest.files)
  if (mode !== "slides") return <SlidesMessage alert={mode === "unsupported"} text={mode === "unsupported" ? "This Slides format is unsupported." : "Slides preview is unavailable."} />
  return <SlidesDeckView key={data.page_id} workspaceId={workspaceId} slug={slug} data={data} canWrite={canEdit} setViewing={setViewing} />
}

function SlidesMessage({ text, alert, action }: { text: string; alert?: boolean; action?: React.ReactNode }) {
  return <div className="grid content-start justify-items-start gap-2 p-6 pt-16 text-sm">
    <p role={alert ? "alert" : "status"} className="text-muted-foreground">{text}</p>{action}
  </div>
}

function useGoFont(enabled: boolean): "loading" | "ready" | "error" {
  const [font, setFont] = React.useState<"loading" | "ready" | "error">("loading")
  React.useEffect(() => {
    if (!enabled) return
    let active = true
    if (!document.fonts) { setFont("error"); return }
    void document.fonts.load('16px "Go Regular"').then(faces => { if (active) setFont(faces.length ? "ready" : "error") }).catch(() => { if (active) setFont("error") })
    return () => { active = false }
  }, [enabled])
  return font
}

function SlidesDeckView({ workspaceId, slug, data, canWrite, setViewing }: { workspaceId: string; slug: string; data: ArtifactProjectSnapshot; canWrite: boolean; setViewing: (version: number | undefined) => void }) {
  const slides = useArtifactSlides({ workspaceId, pageId: data.page_id, codeVersion: data.manifest.version, artifactKind: data.artifact_kind })
  const font = useGoFont(slides.enabled)
  const ready = slides.projection && font === "ready" ? slides.projection : null
  const message = !slides.enabled ? "Slides preview is unavailable."
    : slides.error ? (slides.error instanceof Error && slides.error.message === "Slides response exceeds the data limit" ? "Slides exceeds the preview data limit." : slides.error instanceof Error && slides.error.message === "This Slides format is unsupported." ? slides.error.message : "Slides preview is unavailable. Reload to check access.")
      : slides.projection && font === "error" ? "Slides font is unavailable." : null
  return <>
    {ready ? <SlidesStage projection={ready} codeVersion={data.manifest.version} download={slides.downloadPDF} data={data} workspaceId={workspaceId} slug={slug} canWrite={canWrite} setViewing={setViewing} />
      : message ? <SlidesMessage alert text={message} /> : <div aria-busy="true" aria-label="Loading slides" className="flex flex-1 flex-col p-6 pt-16"><Skeleton className="mx-auto aspect-video w-full max-w-4xl" /></div>}
    {!ready && <ProjectVersionHistory data={data} workspaceId={workspaceId} slug={slug} canWrite={canWrite} setViewing={setViewing} />}
  </>
}

/** Measures the stage so Fit can size the slide; zero until the browser lays it out. */
function useBox(ref: React.RefObject<HTMLElement | null>) {
  const [box, setBox] = React.useState({ width: 0, height: 0 })
  React.useLayoutEffect(() => {
    const element = ref.current
    if (!element) return
    const measure = () => setBox(previous => previous.width === element.clientWidth && previous.height === element.clientHeight ? previous : { width: element.clientWidth, height: element.clientHeight })
    measure()
    if (typeof ResizeObserver === "undefined") return
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [ref])
  return box
}

/** Focus in these keeps its own keys: typing, menus, dialogs and other composite widgets. */
const VIEWER_KEYS_IGNORED = "input,textarea,select,[contenteditable]:not([contenteditable=false]),[role=dialog],[role=menu],[role=menubar],[role=listbox],[role=tablist],[role=radiogroup],[role=slider],[role=textbox],[role=grid],[role=tree]"

const SLIDE_LINK = /^#slide-([A-Za-z][A-Za-z0-9_-]{0,63})$/

/** The position of the slide `#slide-<id>` names, or -1 when the link names none of these slides. */
export function linkedSlide(order: readonly string[], hash = typeof window === "undefined" ? "" : window.location.hash): number {
  const match = SLIDE_LINK.exec(hash)
  return match ? order.indexOf(match[1]) : -1
}

/**
 * The selected slide lives in the address as `#slide-<id>`: reload, a shared
 * link and back/forward select it. The fragment is replaced, not pushed, so
 * stepping through a deck does not fill the history; the query (workspace,
 * pinned version, edit) is left as it is.
 */
function useSlideLink(order: readonly string[], slideId: string | undefined, select: (position: number) => void) {
  React.useEffect(() => {
    if (!slideId || typeof window === "undefined") return
    const hash = `#slide-${slideId}`
    if (window.location.hash !== hash) window.history.replaceState(window.history.state, "", `${window.location.pathname}${window.location.search}${hash}`)
  }, [slideId])
  const follow = React.useRef(select); follow.current = select
  React.useEffect(() => {
    const onNavigate = () => { const position = linkedSlide(order); if (position >= 0) follow.current(position) }
    window.addEventListener("hashchange", onNavigate)
    window.addEventListener("popstate", onNavigate)
    return () => { window.removeEventListener("hashchange", onNavigate); window.removeEventListener("popstate", onNavigate) }
  }, [order])
}

interface StageProps {
  projection: ArtifactSlidesProjection
  codeVersion: number
  download: () => Promise<Uint8Array<ArrayBuffer> | null>
  data: ArtifactProjectSnapshot
  workspaceId: string
  slug: string
  canWrite: boolean
  setViewing: (version: number | undefined) => void
}

function SlidesStage({ projection, codeVersion, download, data, workspaceId, slug, canWrite, setViewing }: StageProps) {
  const { deck, anchors } = projection
  const frame = useArtifactFrame()
  const phone = frame?.layout === "phone"
  // One selection for the stage, filmstrip, grid, picker, notes and Present; the link names it.
  const [current, setCurrent] = React.useState(() => Math.max(0, linkedSlide(deck.slide_order)))
  const index = Math.min(current, deck.slide_order.length - 1)
  const slideId = deck.slide_order[index]
  const slide = deck.slides.find(entry => entry.id === slideId)
  const [notes, setNotes] = React.useState(false)
  const [grid, setGrid] = React.useState(false)
  const [strip, setStrip] = React.useState(true)
  const [zoom, setZoom] = React.useState<"fit" | number>("fit")
  const [present, setPresent] = React.useState(false)
  const [pdfPending, setPdfPending] = React.useState(false)
  const [pdfError, setPdfError] = React.useState("")
  const [overflow, setOverflow] = React.useState<string[]>([])
  const onOverflow = React.useCallback((ids: string[]) => setOverflow(ids), [])
  const [selectMode, setSelectMode] = React.useState(false)
  const [anchor, setAnchor] = React.useState<{ version: number; path: string; selector: string; quote: string } | null>(null)
  const [selectionError, setSelectionError] = React.useState("")
  const mounted = React.useRef(true)
  React.useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const area = React.useRef<HTMLDivElement>(null)
  const box = useBox(area)
  const pad = phone ? 16 : 40
  const fit = box.width ? Math.max(0.05, Math.min((box.width - pad * 2) / deck.width, (box.height - pad * 2) / deck.height)) : 0.5
  const scale = zoom === "fit" ? fit : zoom
  const step = (direction: 1 | -1) => setZoom(() => {
    const next = direction > 0 ? ZOOM_STEPS.find(value => value > scale + 0.001) : [...ZOOM_STEPS].reverse().find(value => value < scale - 0.001)
    return next ?? (direction > 0 ? ZOOM_STEPS.at(-1)! : ZOOM_STEPS[0])
  })
  const thumbnails = React.useMemo(() => deck.slide_order.map(id => ({ id, deck: artifactSlideThumbnail(deck, id) })), [deck])
  const go = (next: number) => { setCurrent(Math.max(0, Math.min(deck.slide_order.length - 1, next))); setSelectionError("") }
  const viewingOld = data.manifest.version !== data.latest_version
  const canComment = canWrite || data.can_comment === true
  const select = (id: string) => {
    const path = `slides/${slideId}.html`
    if (!selectMode || !Object.hasOwn(anchors, id) || anchors[id].slide_id !== slideId || anchors[id].path !== path) { setSelectionError("Choose authored text or a table to comment on."); return }
    setSelectionError("")
    setAnchor({ version: data.manifest.version, path, selector: anchors[id].selector, quote: anchors[id].quote })
  }
  useSlideLink(deck.slide_order, slideId, setCurrent)
  // Arrows, PageUp/PageDown, Home and End move the viewer's selection; Present has its own keys.
  React.useEffect(() => {
    if (present) return
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
      const target = event.target instanceof Element ? event.target : null
      if (target?.closest(VIEWER_KEYS_IGNORED)) return
      const last = deck.slide_order.length - 1
      const next = event.key === "ArrowRight" || event.key === "PageDown" ? index + 1 : event.key === "ArrowLeft" || event.key === "PageUp" ? index - 1
        : event.key === "Home" ? 0 : event.key === "End" ? last : null
      if (next === null) return
      event.preventDefault()
      setCurrent(Math.max(0, Math.min(last, next))); setSelectionError("")
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [present, index, deck.slide_order.length])
  // Leaving browser full screen (Escape there never reaches the page) ends Present on the slide shown.
  React.useEffect(() => {
    if (!present || typeof document === "undefined") return
    let entered = Boolean(document.fullscreenElement)
    const onChange = () => { if (document.fullscreenElement) entered = true; else if (entered) setPresent(false) }
    document.addEventListener("fullscreenchange", onChange)
    return () => document.removeEventListener("fullscreenchange", onChange)
  }, [present])
  const startPresent = () => { setPresent(true); enterFullscreen() }
  const stopPresent = () => { setPresent(false); exitFullscreen() }
  const pdf = async () => {
    if (pdfPending) return
    setPdfPending(true); setPdfError("")
    try {
      const bytes = await download()
      if (!bytes || !mounted.current) return
      const url = URL.createObjectURL(new Blob([bytes], { type: "application/pdf" }))
      try {
        const link = document.createElement("a")
        link.href = url; link.download = `slides-v${codeVersion}.pdf`; link.click()
      } finally { URL.revokeObjectURL(url) }
    } catch (error) { if (mounted.current && (error as { name?: string })?.name !== "AbortError") setPdfError(error instanceof Error && error.message === "Slides response exceeds the data limit" ? "PDF exceeds the download data limit." : "PDF download is unavailable. Reload to check access or slide overflow.") }
    finally { if (mounted.current) setPdfPending(false) }
  }
  const count = `${index + 1} / ${deck.slide_order.length}`
  return <section aria-label="Slides" data-slides-layout={phone ? "phone" : "wide"} className="artifact-slides relative flex min-h-0 flex-1 flex-col bg-muted/40">
    {(pdfError || selectionError || overflow.length > 0) && <div className={cn("relative z-[11] mx-3 grid gap-1 rounded-card border bg-card px-3 py-2 text-sm", phone ? "mt-16" : "mt-14")}>
      {pdfError && <p role="alert">{pdfError}</p>}
      {selectionError && <p role="status">{selectionError}</p>}
      {overflow.length > 0 && <p role="status" className="text-muted-foreground">Some slide content overflows its frame. PDF export may be unavailable.</p>}
    </div>}
    {grid ? <div className={cn("min-h-0 flex-1 overflow-auto px-4 pb-16", phone ? "pt-16" : "pt-14")}>
      <ol aria-label="All slides" className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-3">
        {thumbnails.map(({ id, deck: thumbnail }, position) => <li key={id}>
          <button type="button" aria-label={`Slide ${position + 1}: ${slideLabel(deck.slides.find(entry => entry.id === id), position)}`} aria-current={position === index ? "true" : undefined}
            onClick={() => { go(position); setGrid(false) }}
            className="grid w-full gap-1 rounded-card border-2 border-transparent p-1 text-left aria-[current=true]:border-primary">
            <span className="overflow-hidden rounded-[var(--radius-row)] shadow-sm"><SlideCanvas deck={thumbnail} slideId={id} /></span>
            <small className="truncate text-xs text-muted-foreground">{position + 1} · {slideLabel(deck.slides.find(entry => entry.id === id), position)}</small>
          </button>
        </li>)}
      </ol>
    </div> : <div ref={area} className={cn("relative min-h-0 flex-1 overflow-auto", phone ? "pt-16" : "pt-12", !strip && !notes && (phone ? "pb-28" : "pb-12"))}>
      <div className="flex min-h-full min-w-full items-center justify-center" style={{ padding: pad, width: zoom === "fit" ? undefined : deck.width * scale + pad * 2 }}>
        <div data-slide-stage className="artifact-slides-main shrink-0 overflow-hidden rounded-card shadow-lg" style={{ width: deck.width * scale }}>
          <SlideCanvas deck={deck} slideId={slideId} onSelectElement={canComment && selectMode && !viewingOld ? select : undefined} onOverflowChange={onOverflow} />
        </div>
      </div>
    </div>}
    {notes && !grid && <aside aria-label="Speaker notes" className={cn("mx-3 max-h-40 shrink-0 overflow-auto rounded-card border bg-card px-3 py-2 text-sm", !strip && (phone ? "mb-28" : "mb-14"))}>
      <b className="flex items-center gap-1.5 text-xs font-semibold text-muted-foreground"><NotebookText aria-hidden className="size-3.5" />Speaker notes · slide {index + 1}</b>
      <p className="mt-1 whitespace-pre-wrap">{slide?.notes.trim() || "No notes for this slide."}</p>
    </aside>}
    {strip && !grid && <nav aria-label="Slide thumbnails" data-filmstrip
      className={cn("mx-auto flex shrink-0 gap-2 overflow-x-auto px-3 pt-2 [scrollbar-width:thin]", phone ? "mb-[7.5rem]" : "mb-3 w-fit max-w-[calc(100%-26rem)]")}>
      {thumbnails.map(({ id, deck: thumbnail }, position) => <button type="button" key={id} aria-label={`Slide ${position + 1}`} aria-current={position === index ? "true" : undefined}
        onClick={() => go(position)} className="relative w-28 shrink-0 overflow-hidden rounded-[var(--radius-row)] border-2 border-transparent bg-card shadow-sm aria-[current=true]:border-primary coarse:min-h-[44px] md:w-32">
        <span className="absolute left-0 top-0 z-[1] rounded-br-[var(--radius-row)] bg-foreground px-1 font-mono text-xs text-background">{position + 1}</span>
        <SlideCanvas deck={thumbnail} slideId={id} />
      </button>)}
    </nav>}

    <ArtifactSlot name="view" label="View">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button" aria-label={`Slide ${index + 1} of ${deck.slide_order.length}, choose a slide`} className="inline-flex h-[30px] max-w-[60vw] items-center gap-1.5 rounded-[var(--radius-row)] px-2 text-sm hover:bg-accent coarse:min-h-[44px]">
            <Presentation aria-hidden className="size-3.5 text-muted-foreground" />
            <b className="truncate font-semibold">{slideLabel(slide, index)}</b>
            <span className="font-mono text-xs text-muted-foreground">{count}</span>
            <ChevronDown aria-hidden className="size-3 shrink-0 text-muted-foreground" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="max-h-80 w-64 overflow-y-auto">
          <DropdownMenuLabel className="text-xs text-muted-foreground">{deck.slide_order.length} slides</DropdownMenuLabel>
          {deck.slide_order.map((id, position) => <DropdownMenuItem key={id} onSelect={() => { go(position); setGrid(false) }} aria-current={position === index ? "true" : undefined}>
            <span className="w-5 font-mono text-xs text-muted-foreground">{position + 1}</span>
            <span className="min-w-0 flex-1 truncate">{slideLabel(deck.slides.find(entry => entry.id === id), position)}</span>
          </DropdownMenuItem>)}
        </DropdownMenuContent>
      </DropdownMenu>
    </ArtifactSlot>
    <ArtifactSlot name="output" label="Output">
      {/* Only PDF has a server route; PowerPoint and PNG are hidden until one exists. */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild><ArtifactTool icon={Download} label="Download" disabled={pdfPending} /></DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-56">
          <DropdownMenuItem onSelect={() => void pdf()} disabled={pdfPending}><Download aria-hidden />{pdfPending ? "Preparing PDF…" : "PDF"}<span className="ml-auto text-xs text-muted-foreground">v{codeVersion}</span></DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ArtifactTool icon={Play} label="Present" text="Present" onClick={startPresent} />
    </ArtifactSlot>
    <ArtifactSlot name="viewport" label="Zoom">
      <ArtifactTool icon={ZoomOut} label="Zoom out" disabled={grid} onClick={() => step(-1)} />
      <button type="button" aria-label="Fit the slide" aria-pressed={zoom === "fit"} disabled={grid} onClick={() => setZoom("fit")}
        className="h-[30px] min-w-[48px] rounded-[var(--radius-row)] px-1.5 font-mono text-xs text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40">{zoom === "fit" ? "Fit" : `${Math.round(scale * 100)} %`}</button>
      <ArtifactTool icon={ZoomIn} label="Zoom in" disabled={grid} onClick={() => step(1)} />
    </ArtifactSlot>
    <ArtifactSlot name="nav" label="Slides">
      <ArtifactTool icon={NotebookText} label="Speaker notes" on={notes} disabled={grid} onClick={() => setNotes(value => !value)} />
      <ArtifactTool icon={grid ? Square : LayoutGrid} label={grid ? "Single slide" : "All slides"} on={grid} onClick={() => setGrid(value => !value)} />
      <ArtifactTool icon={Film} label={strip ? "Hide filmstrip" : "Show filmstrip"} on={strip} disabled={grid} onClick={() => setStrip(value => !value)} />
      <ArtifactToolSeparator />
      <span aria-live="polite" className="px-1.5 font-mono text-xs text-muted-foreground">{count}</span>
    </ArtifactSlot>

    <SlidesComments data={data} workspaceId={workspaceId} slug={slug} canWrite={canWrite} canComment={canComment} viewingOld={viewingOld}
      selectMode={selectMode} setSelectMode={setSelectMode} anchor={anchor} setAnchor={setAnchor}
      onOpen={comment => { const position = deck.slide_order.findIndex(id => comment.path === `slides/${id}.html`); if (position >= 0) { go(position); setGrid(false) } }} />
    <ProjectVersionHistory data={data} workspaceId={workspaceId} slug={slug} canWrite={canWrite} setViewing={setViewing} />
    {present && <SlidePresent deck={deck} initialSlideId={slideId} onClose={stopPresent}
      onSlideChange={id => { const position = deck.slide_order.indexOf(id); if (position >= 0) setCurrent(position) }} />}
  </section>
}

interface CommentsProps {
  data: ArtifactProjectSnapshot
  workspaceId: string
  slug: string
  canWrite: boolean
  canComment: boolean
  viewingOld: boolean
  selectMode: boolean
  setSelectMode: (on: boolean) => void
  anchor: { version: number; path: string; selector: string; quote: string } | null
  setAnchor: (anchor: null) => void
  onOpen: (comment: ArtifactComment) => void
}

/**
 * Comments on authored text or a table, over the existing artifact comment
 * route. Edit with an agent chat may send one to the agent (Propose); Comment
 * saves plain feedback. The unsent text is kept per person and deck, like the
 * project viewer's.
 */
function SlidesComments({ data, workspaceId, slug, canWrite, canComment, viewingOld, selectMode, setSelectMode, anchor, setAnchor, onOpen }: CommentsProps) {
  const me = useSessionSafe()?.data?.user?.id
  const maySend = canWrite && data.can_send_to_agent === true
  const agent = useArtifactAgentDispatch(workspaceId, data.page_id, data.origin ?? undefined, maySend)
  const { comment } = useArtifactProjectMutations(workspaceId, slug)
  const draftKey = artifactDraftKey("artifact-viewer-comment", me, workspaceId, data.page_id)
  const [draftState, setDraftState] = React.useState<{ key: string | null; text: string }>({ key: null, text: "" })
  const draft = draftState.key === draftKey ? draftState.text : ""
  const [notice, setNotice] = React.useState("")
  React.useEffect(() => {
    if (!draftKey) return
    let live = true
    void loadArtifactDraft(draftKey).then(text => { if (live) setDraftState(previous => previous.key === draftKey ? previous : { key: draftKey, text }) }, () => { if (live) setDraftState({ key: draftKey, text: "" }) })
    return () => { live = false }
  }, [draftKey])
  const setDraft = (text: string) => {
    if (!draftKey || draftState.key !== draftKey) return
    setDraftState({ key: draftKey, text })
    void writeArtifactDraft(draftKey, text).catch(() => { /* the in-memory draft remains */ })
  }
  const submit = (send: boolean) => {
    if (!draftKey || draftState.key !== draftKey || !anchor || !draft.trim() || viewingOld || (send && !maySend)) return
    const submitted = { key: draftKey, text: draft, revision: artifactDraftRevision(draftKey) }
    setNotice("")
    comment.mutate({ ...anchor, text: draft.trim(), send_to_agent: send }, {
      onSuccess: async saved => {
        void clearArtifactDraft(submitted.key, submitted.text, submitted.revision).catch(() => {})
        setDraftState(previous => previous.key === submitted.key && previous.text === submitted.text ? { key: submitted.key, text: "" } : previous)
        setAnchor(null); setSelectMode(false)
        if (!send) { setNotice("Comment saved"); return }
        if (!data.origin) { setNotice("Comment saved for the agent"); return }
        setNotice("Sending to agent…")
        const accepted = await agent.dispatch(saved).catch(() => false)
        setNotice(accepted ? "Sent to agent" : agent.refusal?.(saved.id) === "busy" ? "Comment saved. The agent is busy; retry when it finishes." : "Comment saved. Retry when the source chat is connected.")
      },
      onError: error => setNotice(error.code === "artifact_version_conflict" ? "The deck changed meanwhile. Your comment is kept; reload the latest version and select again." : error.message || "The comment could not be saved. Your text is kept."),
    })
  }
  const composer = (label: string, send: boolean) => <div className="grid gap-2">
    <Button type="button" size="sm" variant={selectMode ? "secondary" : "outline"} aria-pressed={selectMode} disabled={viewingOld} onClick={() => setSelectMode(!selectMode)}>
      <MousePointerClick aria-hidden />{selectMode ? "Selecting on the slide…" : "Select on the slide"}
    </Button>
    <p className="text-xs text-muted-foreground">{viewingOld ? "Comments go on the latest version." : anchor ? <>On “{anchor.quote}”</> : "Select authored text or a table on the slide."}</p>
    <Textarea aria-label={label} value={draft} disabled={!draftKey || draftState.key !== draftKey} onChange={event => setDraft(event.target.value)} placeholder="What should change?" className="min-h-20 text-sm" />
    <div className="flex justify-end">
      <Button type="button" size="sm" disabled={!anchor || !draft.trim() || comment.isPending || viewingOld} onClick={() => submit(send)}>{send ? "Send to agent" : "Save comment"}</Button>
    </div>
    {notice && <p role="status" className="text-xs text-muted-foreground">{notice}</p>}
  </div>
  const comments = [...data.comments].reverse()
  return <>
    <ArtifactDrawerSection tab="comments" label="Comments">
      <div className="grid content-start gap-3 p-3.5">
        {canComment ? composer("Comment on the slide", false) : <p className="text-xs text-muted-foreground">You can read comments. Commenting needs Comment or Edit access.</p>}
        {comments.length === 0 ? <p className="text-sm text-muted-foreground">No comments yet.</p> : <ul className="grid gap-2">
          {comments.map(entry => {
            const chip = commentQueueChip(entry)
            return <li key={entry.id} className="rounded-card border p-2.5 text-sm">
              <button type="button" className="text-left text-xs text-muted-foreground hover:underline" onClick={() => onOpen(entry)}>v{entry.version} · {entry.quote ? `“${entry.quote}”` : entry.path}</button>
              <p className="mt-1 whitespace-pre-wrap">{entry.text}</p>
              {chip && <div className="mt-1.5"><StatusPill tone={chip.tone} live={chip.live} label={chip.label} /></div>}
            </li>
          })}
        </ul>}
      </div>
    </ArtifactDrawerSection>
    {maySend && data.origin && <ArtifactAgentMode mode="propose">
      <div className="grid gap-2">
        <p className="text-xs text-muted-foreground">Select text on a slide and describe the change. The agent answers in its chat with a new version.</p>
        {composer("Request for the agent", true)}
      </div>
    </ArtifactAgentMode>}
  </>
}

/** Versions from the snapshot (the owner sees the list); View, Restore and Share use the existing routes against the latest version. */
export function ProjectVersionHistory({ data, workspaceId, slug, canWrite, setViewing }: { data: ArtifactProjectSnapshot; workspaceId: string; slug: string; canWrite: boolean; setViewing: (version: number | undefined) => void }) {
  const { restore, share } = useArtifactProjectMutations(workspaceId, slug)
  const [notice, setNotice] = React.useState("")
  const [confirmShare, setConfirmShare] = React.useState<number | null>(null)
  const latest = data.latest_version
  const shown = data.manifest.version
  const mayRestore = canWrite && data.can_restore === true
  const mayShare = canWrite && data.can_share === true
  const fail = (error: { code?: string; message?: string }) => setNotice(error.code === "artifact_version_conflict" ? "This artifact changed meanwhile. Nothing was changed; check the latest version." : error.message || "That did not work. Nothing was changed.")
  const versions = data.versions.length ? [...data.versions].reverse() : [{ version: shown }]
  return <ArtifactDrawerSection tab="history" label="History">
    <div className="grid content-start gap-2 p-3.5">
      {shown !== latest && <Button type="button" size="sm" variant="secondary" onClick={() => setViewing(undefined)}>Back to latest · v{latest}</Button>}
      {notice && <p role="status" className="text-xs text-muted-foreground">{notice}</p>}
      <ul className="grid gap-2">
        {versions.map(entry => <li key={entry.version} aria-current={entry.version === shown ? "true" : undefined} className="rounded-card border p-2.5 text-sm aria-[current=true]:border-primary/50">
          <div className="flex flex-wrap items-center gap-1.5">
            <b className="font-mono font-semibold">v{entry.version}</b>
            {entry.version === latest && <StatusPill tone="muted" label="Latest" />}
            {entry.version === data.shared_version && <StatusPill tone="success" label="Shared" />}
          </div>
          {"message" in entry && entry.message && <p className="mt-1 text-xs text-muted-foreground">{entry.message}</p>}
          {"created_at" in entry && entry.created_at && <p className="mt-0.5 text-xs text-muted-foreground">{new Date(entry.created_at).toLocaleString()}</p>}
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {entry.version !== shown && <Button type="button" size="sm" variant="ghost" onClick={() => setViewing(entry.version === latest ? undefined : entry.version)}>View</Button>}
            {mayRestore && entry.version !== latest && <Button type="button" size="sm" variant="ghost" disabled={restore.isPending}
              onClick={() => restore.mutate({ base_version: latest, version: entry.version }, { onSuccess: result => { setNotice(`Restored v${entry.version} as v${result.version} · nothing deleted`); setViewing(undefined) }, onError: fail })}>Restore as new version</Button>}
            {mayShare && entry.version !== data.shared_version && (confirmShare === entry.version
              ? <Button type="button" size="sm" variant="secondary" disabled={share.isPending}
                onClick={() => share.mutate({ base_version: latest, version: entry.version }, { onSuccess: result => { setConfirmShare(null); setNotice(`Shared v${result.shared_version}`) }, onError: error => { setConfirmShare(null); fail(error) } })}>Share v{entry.version} with readers</Button>
              : <Button type="button" size="sm" variant="ghost" onClick={() => setConfirmShare(entry.version)}>Share…</Button>)}
          </div>
        </li>)}
      </ul>
      {!data.versions.length && <p className="text-xs text-muted-foreground">Only the owner sees the full version list.</p>}
    </div>
  </ArtifactDrawerSection>
}

