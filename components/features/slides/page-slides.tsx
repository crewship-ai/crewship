"use client"

import { usePageSlides, type PageSlidesInput } from "@/hooks/use-page-slides"
import { SlidesEditor } from "./slides-editor"

/** Displays a verified saved revision; never exposes a candidate save action. */
export function PageSlides(props: PageSlidesInput) {
  const { scopeKey, read } = usePageSlides(props)
  if (read.status === "loading") return <p role="status">Loading saved Slides…</p>
  if (read.status !== "ready") return <p role="alert">{read.status === "denied" ? "Saved Slides are not accessible." : "Saved Slides are unavailable."}</p>
  return <section aria-label="Saved Slides" className="space-y-3 p-4">
    <SlidesEditor key={`${scopeKey}:${read.state.revision}`} deck={read.state.deck} readOnly persistenceStatus={`Saved revision ${read.state.revision}; read only`} />
  </section>
}
