"use client"
import { useState } from "react"
import { usePages } from "@/hooks/use-pages"
import { usePageArtifact } from "@/hooks/use-page-artifact"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"
export function BoardPinDialog({ workspaceId, pending, error, onClose, onPin }: { workspaceId: string; pending: boolean; error: string | null; onClose: () => void; onPin: (slug: string, revision: number) => void }) {
  const { pages } = usePages(workspaceId)
  const [slug, setSlug] = useState("")
  const [selectedRevision, setSelectedRevision] = useState<number | null>(null)
  const { query } = usePageArtifact(workspaceId, slug)
  const revision = selectedRevision ?? query.data?.head_rev
  return <Dialog open onOpenChange={open => { if (!open && !pending) onClose() }}><DialogContent><DialogHeader><DialogTitle>Pin artifact</DialogTitle><DialogDescription>Pin a saved revision. The board does not grant access to its source, and the preview does not run scripts.</DialogDescription></DialogHeader>
    <div className="space-y-4"><div className="space-y-2"><label htmlFor="board-pin-source" className="text-sm font-medium">Artifact</label><select id="board-pin-source" className="w-full rounded-md border bg-background p-2 text-sm coarse:min-h-12" value={slug} disabled={pending} onChange={event => { setSlug(event.target.value); setSelectedRevision(null) }}><option value="">Choose an artifact</option>{pages.filter(page => page.hasArtifact).map(page => <option key={page.slug} value={page.slug}>{page.name}</option>)}</select></div>
      {slug && <div className="space-y-2"><label htmlFor="board-pin-revision" className="text-sm font-medium">Saved revision</label><select id="board-pin-revision" className="w-full rounded-md border bg-background p-2 text-sm coarse:min-h-12" value={revision ?? ""} disabled={pending || !query.data || Boolean(query.error)} onChange={event => setSelectedRevision(Number(event.target.value))}>{query.data?.revisions.map(item => <option key={item.revision} value={item.revision}>Revision {item.revision}{item.revision === query.data.head_rev ? " · latest" : ""}</option>)}</select></div>}
      {query.error && <p role="alert" className="text-sm text-destructive">Artifact access could not be confirmed. Choose another source.</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </div><DialogFooter><Button variant="ghost" disabled={pending} onClick={onClose}>Cancel</Button><Button disabled={pending || !slug || !revision || Boolean(query.error)} onClick={() => onPin(slug, revision!)}>{pending ? "Pinning…" : "Pin saved revision"}</Button></DialogFooter>
  </DialogContent></Dialog>
}
