"use client"

import { PageArtifactError, usePageArtifact } from "@/hooks/use-page-artifact"
import { ArtifactSourceDiff } from "./artifact-source-diff"
import { useStaticArtifactDocument } from "@/hooks/use-static-artifact-document"

function RevisionPanel({ workspaceId, slug, revision }: { workspaceId: string; slug: string; revision: number }) {
  const { query } = usePageArtifact(workspaceId, slug, revision)
  // Historical placeholder data must never appear under a different label.
  const data = query.data?.revision === revision ? query.data : undefined
  const denied = query.error instanceof PageArtifactError && [401, 403, 404].includes(query.error.status)
  const document = useStaticArtifactDocument(data && !denied ? data.html : undefined)
  if (query.error) return <p role="alert" className="p-4 text-sm">Revision {revision}: {query.error.message}</p>
  if (!data) return <p role="status" className="p-4 text-sm">Loading revision {revision}…</p>
  const sourceLimit = 200000
  return <div className="min-w-0 overflow-hidden rounded-lg border">
    <iframe key={document ? "ready" : "pending"} title={`Compare revision ${revision}`} sandbox="" referrerPolicy="no-referrer" srcDoc={document} className="h-96 w-full border-0 bg-white" />
    <details className="border-t p-3"><summary className="cursor-pointer text-sm">Source · revision {revision}</summary>
      {data.html.length > sourceLimit && <p className="py-2 text-xs text-muted-foreground">Showing the first {sourceLimit.toLocaleString()} characters. Open this revision in Source to download the complete document.</p>}
      <pre className="mt-3 max-h-96 overflow-auto whitespace-pre-wrap break-all text-xs">{data.html.slice(0, sourceLimit)}</pre>
    </details>
  </div>
}

export function ArtifactCompare({ workspaceId, slug, revisions, comparison, onComparisonChange }: { workspaceId: string; slug: string; revisions: number[]; comparison: { before: number; after: number }; onComparisonChange: (comparison: { before: number; after: number }) => void }) {
  const { before: left, after: right } = comparison
  return <div className="space-y-3 p-4">
    <p className="text-sm text-muted-foreground">Compare two saved revisions side by side. Both previews use the same static safety rules; scripts do not run.</p>
    <div className="grid gap-4 md:grid-cols-2">
      {([{ side: "Earlier", value: left, change: (before: number) => onComparisonChange({ ...comparison, before }) }, { side: "Later", value: right, change: (after: number) => onComparisonChange({ ...comparison, after }) }] as const).map(({ side, value, change }) => <div key={side} className="min-w-0 space-y-3">
        <label className="flex items-center gap-2 text-sm">{side} revision<select aria-label={`${side} revision`} value={value} onChange={event => change(Number(event.target.value))} className="rounded-md border bg-background px-3 py-2 coarse:min-h-12">{[...revisions].sort((a, b) => b - a).map(revision => <option key={revision} value={revision}>Revision {revision}</option>)}</select></label>
        <RevisionPanel workspaceId={workspaceId} slug={slug} revision={value} />
      </div>)}
    </div>
    <ArtifactSourceDiff workspaceId={workspaceId} slug={slug} before={left} after={right} />
  </div>
}
