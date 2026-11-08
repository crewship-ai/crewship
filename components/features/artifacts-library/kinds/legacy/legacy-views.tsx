"use client"

import * as React from "react"
import { slidesEditorMount } from "@/lib/feature-flags"
import { createHttpSlidesClient } from "@/lib/slides/http-client"
import type { SlideDeck } from "@/lib/slides/model"
import { PageSlides } from "@/components/features/pages/page-slides"
import { PageWorkbookSurface } from "@/components/features/pages/workbook-editor-host"
import { SlideCanvas } from "@/components/features/slides/slide-canvas"
import { orderedSlides } from "@/components/features/slides/native-model"

/**
 * Older storage that the Pages route used to show, now hosted in the
 * Artifacts frame instead of a link out to /pages (owner, 8 Nov 2026). Each
 * body is the same component Pages mounted for it, so permissions, drafts and
 * reads are unchanged; only where it renders moved.
 */

type DeckRead = { state: "loading" } | { state: "ready"; deck: SlideDeck } | { state: "failed"; message: string }

/** The saved deck, read-only: every slide as the canvas draws it, nothing editable, nothing written. */
export function SavedDeckPreview({ workspaceId, pageId }: { workspaceId: string; pageId: string }) {
  const client = React.useMemo(() => createHttpSlidesClient(workspaceId), [workspaceId])
  const [read, setRead] = React.useState<DeckRead>({ state: "loading" })
  React.useEffect(() => {
    let live = true
    setRead({ state: "loading" })
    client.load(pageId).then(result => {
      if (!live) return
      if ("deck" in result) setRead({ state: "ready", deck: result.deck })
      else setRead({ state: "failed", message: result.status === "unavailable" && result.reason === "not_implemented"
        ? "Slides are not available on this server yet, so there is no preview. Nothing was changed."
        : "The deck could not be loaded. Nothing was changed." })
    }, () => { if (live) setRead({ state: "failed", message: "The deck could not be loaded. Nothing was changed." }) })
    return () => { live = false }
  }, [client, pageId])
  if (read.state === "loading") return <div aria-busy="true" className="m-4 h-40 animate-pulse rounded-card border bg-muted/40 md:m-6" />
  if (read.state === "failed") return <p role="status" className="m-4 rounded-card border border-dashed p-4 text-sm text-muted-foreground md:m-6">{read.message}</p>
  const slides = orderedSlides(read.deck)
  return <section aria-label="Saved deck" className="mx-auto grid w-full max-w-4xl content-start gap-4 p-4 md:p-6">
    {slides.map((slide, index) => <figure key={slide.id} className="grid gap-1.5">
      <div className="overflow-hidden rounded-card border bg-card"><SlideCanvas deck={read.deck} slideId={slide.id} /></div>
      <figcaption className="text-xs text-muted-foreground">Slide {index + 1} of {slides.length}</figcaption>
    </figure>)}
  </section>
}

/**
 * A deck stored before typed artifacts: the Slides editor where its build
 * switch mounts it (the same editor Pages mounted, a local draft only), the
 * saved deck read-only everywhere else.
 */
export function LegacyDeckBody({ workspaceId, pageId, name }: { workspaceId: string; pageId: string; name: string }) {
  if (slidesEditorMount()) return <div className="flex min-h-0 flex-1 flex-col"><PageSlides workspaceId={workspaceId} pageId={pageId} name={name} /></div>
  return <SavedDeckPreview workspaceId={workspaceId} pageId={pageId} />
}

/** A workbook stored before native Craft: the read-only preview, or the embedded editor where the workspace turned it on, as on Pages. */
export function LegacyWorkbookBody({ workspaceId, pageId, name }: { workspaceId: string; pageId: string; name: string }) {
  return <div className="flex min-h-0 flex-1 flex-col"><PageWorkbookSurface key={`${workspaceId}:${pageId}`} workspaceId={workspaceId} pageId={pageId} name={name} /></div>
}
