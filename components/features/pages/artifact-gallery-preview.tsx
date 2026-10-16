"use client"

import { usePageArtifact } from '@/hooks/use-page-artifact'
import { useStaticArtifactDocument } from '@/hooks/use-static-artifact-document'

/** Mounted only for the one card the reader explicitly opens. */
export function ArtifactGalleryPreview({ workspaceId, slug, name }: { workspaceId: string; slug: string; name: string }) {
  const { query } = usePageArtifact(workspaceId, slug)
  const document = useStaticArtifactDocument(query.data?.html)
  if (query.error) return <p role="alert" className="text-xs text-muted-foreground">Preview unavailable. Open the artifact to check your access and try again.</p>
  if (!query.data) return <p role="status" className="text-xs text-muted-foreground">Loading preview…</p>
  if (!query.data.head_rev) return <p className="text-xs text-muted-foreground">No published revision yet.</p>
  return <div className="overflow-hidden rounded-md border">
    <iframe key={document ? "ready" : "pending"} title={`Static preview of ${name}`} sandbox="" referrerPolicy="no-referrer" srcDoc={document} className="h-52 w-full border-0 bg-white" />
    <p className="bg-muted/30 px-2 py-1 text-xs text-muted-foreground">Revision {query.data.revision} · Static preview</p>
  </div>
}
