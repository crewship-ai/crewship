"use client"

import { Button } from "@/components/ui/button"
import type { PageArtifact } from "@/hooks/use-page-artifact"

export function ArtifactHistory({ artifact, pending, onView, onRestore }: {
  artifact: PageArtifact
  pending: boolean
  onView: (revision: number) => void
  onRestore: (revision: number) => void
}) {
  return <div className="p-4">
    {artifact.revisions.length === 0 ? <p className="text-sm text-muted-foreground">No revisions yet.</p> : (
      <ol className="divide-y">
        {artifact.revisions.map(item => (
          <li key={item.revision} className="flex flex-wrap items-center gap-3 py-3 text-sm">
            <span className="font-medium">Revision {item.revision}</span>
            <time className="mr-auto text-xs text-muted-foreground" dateTime={item.created_at}>{new Date(item.created_at).toLocaleString()}</time>
            <Button size="sm" variant="outline" disabled={pending} onClick={() => onView(item.revision)}>View</Button>
            {artifact.can_write && item.revision !== artifact.head_rev && (
              <Button size="sm" variant="ghost" disabled={pending} onClick={() => onRestore(item.revision)}>Restore as new revision</Button>
            )}
          </li>
        ))}
      </ol>
    )}
  </div>
}
