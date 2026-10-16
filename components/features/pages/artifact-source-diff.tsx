"use client"

import { useMemo } from "react"
import { usePageArtifact } from "@/hooks/use-page-artifact"
import { unifiedDiffText } from "@/lib/pages/source-diff"

/** Reuse the bounded Pages source diff engine. This is escaped source text,
 * never HTML from either author, and never placeholder data under a new label. */
export function ArtifactSourceDiff({ workspaceId, slug, before, after }: {
  workspaceId: string; slug: string; before: number; after: number
}) {
  const left = usePageArtifact(workspaceId, slug, before).query
  const right = usePageArtifact(workspaceId, slug, after).query
  const a = !left.error && left.data?.revision === before ? left.data.html : undefined
  const b = !right.error && right.data?.revision === after ? right.data.html : undefined
  const diff = useMemo(() => {
    if (a === undefined || b === undefined) return null
    if (a === b) return { identical: true, text: "" }
    // Bound before splitting or diffing: minified HTML can be a single giant
    // line, while newline-heavy content would otherwise allocate millions.
    if (a.length > 200000 || b.length > 200000) return { limited: true, text: "" }
    return { text: unifiedDiffText(a, b, 1200) }
  }, [a, b])
  if (left.error || right.error) return <p role="alert" className="text-sm">Source comparison is unavailable. Both revisions must remain accessible.</p>
  if (!diff) return <p role="status" className="text-sm">Loading source comparison…</p>
  return <details className="rounded-lg border p-3">
    <summary className="cursor-pointer text-sm">Source changes · index.html · revision {before} → {after}</summary>
    {diff.identical ? <p className="pt-3 text-sm text-muted-foreground">The saved HTML is identical.</p> : diff.limited ? <p className="pt-3 text-sm text-muted-foreground">The HTML changed, but exceeds the 200,000-character comparison limit. Open each revision in Source to download the complete documents.</p> : <>
      <p className="pt-3 text-xs text-muted-foreground">− removed, + added. Large changes may be abbreviated.</p>
      <pre aria-label="HTML source changes" className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-all text-xs">{diff.text}</pre>
    </>}
  </details>
}
