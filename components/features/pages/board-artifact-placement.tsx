"use client"
import { useMemo } from "react"
import { usePageArtifact, PageArtifactError } from "@/hooks/use-page-artifact"
import { staticArtifactDocument } from "@/lib/pages/artifact-static"
import type { BoardPlacement } from "@/hooks/use-page-board"
export function BoardArtifactPlacement({ workspaceId, placement }: { workspaceId: string; placement: BoardPlacement | undefined }) {
  const source = placement?.source
  const { query } = usePageArtifact(workspaceId, source?.page_slug ?? "", source?.revision)
  const html = query.data?.html
  const document = useMemo(() => html !== undefined ? staticArtifactDocument(html) : "", [html])
  const denied = query.error instanceof PageArtifactError && [401, 403, 404].includes(query.error.status)
  if (!source || denied) return <div className="flex h-full items-center justify-center bg-muted p-4 text-sm text-muted-foreground">Artifact unavailable</div>
  return <div className="flex h-full flex-col overflow-hidden border bg-background">
    <div className="shrink-0 border-b bg-muted px-3 py-2 text-xs"><span className="font-medium">{source.title}</span> · Revision {source.revision} · Static preview</div>
    {!query.data ? <p className="p-4 text-sm text-muted-foreground">{query.error ? "Preview could not load." : "Loading artifact…"}</p> : <iframe title={`Static artifact: ${source.title}`} sandbox="" referrerPolicy="no-referrer" srcDoc={document} className="min-h-0 flex-1 border-0" style={{ pointerEvents: "none" }} />}
  </div>
}

export function BoardPlacementLink({ workspaceId, placement }: { workspaceId: string; placement: BoardPlacement }) {
  const source = placement.source
  const { query } = usePageArtifact(workspaceId, source?.page_slug ?? "", source?.revision)
  const denied = query.error instanceof PageArtifactError && [401, 403, 404].includes(query.error.status)
  if (!source || denied) return <span className="text-sm text-muted-foreground">Artifact unavailable</span>
  return <div className="flex flex-wrap items-center gap-2 text-sm"><span>{source.title} · Revision {source.revision}</span><a className="text-primary underline" href={`/pages/${encodeURIComponent(source.page_slug)}?revision=${source.revision}`}>Open artifact</a></div>
}
