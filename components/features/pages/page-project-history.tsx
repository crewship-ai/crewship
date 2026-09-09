"use client"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { usePageProjectHistory } from "@/hooks/use-page-project-history"

export function PageProjectHistoryDialog({ workspaceId, slug, onClose }: { workspaceId: string; slug: string; onClose: () => void }) {
  const { query, restore } = usePageProjectHistory(workspaceId, slug)
  const revisions = query.data?.pages.flatMap(page => page.revisions) ?? []
  const current = revisions[0]?.revision
  return <Dialog open onOpenChange={open => { if (!open) onClose() }}>
    <DialogContent className="max-h-[85vh] overflow-auto sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle>Source history</DialogTitle>
        <DialogDescription>Restore an earlier version as a new draft. Your published Page stays active.</DialogDescription>
      </DialogHeader>
      {(query.error || restore.error) && <p role="alert" className="text-sm text-destructive">{query.error?.message ?? restore.error?.message}</p>}
      {restore.isSuccess && <p role="status" className="text-sm">Draft restored. Build a new preview to check it.</p>}
      {query.isPending && <p className="text-sm text-muted-foreground">Loading source history…</p>}
      {!query.isError && revisions.map(revision => <div key={revision.revision} className="flex items-center justify-between gap-3 rounded-md border p-3">
        <div>
          <p className="text-sm font-medium">Revision {revision.revision}{revision.revision === current ? " · Current draft" : ""}</p>
          <p className="text-xs text-muted-foreground">{new Date(revision.created_at).toLocaleString()}{revision.git_commit ? ` · ${revision.git_commit.slice(0, 10)}` : " · Legacy snapshot"}</p>
        </div>
        <Button variant="outline" disabled={!revision.restorable || revision.revision === current || restore.isPending || !current} onClick={() => restore.mutate({ revision: revision.revision, expectedRevision: current })}>Restore draft {revision.revision}</Button>
      </div>)}
      {!query.isPending && !query.isError && revisions.length === 0 && <p className="text-sm text-muted-foreground">This Page has no source revisions yet.</p>}
      {query.hasNextPage && <Button variant="outline" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>Load older revisions</Button>}
    </DialogContent>
  </Dialog>
}
