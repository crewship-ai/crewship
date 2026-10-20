"use client"

/**
 * Share a Document by moving it into a working folder (Q15, CAT-02).
 *
 * The dialog says, before anything is sent, who gains the content and the
 * history, that the item keeps its ID, and that a copy is only made through
 * Duplicate. Several items go one request each, so every item gets its own
 * result: moved, not allowed, changed meanwhile, not found. A conflict is
 * never retried on the person's behalf.
 */

import * as React from "react"

import { Spinner } from "@/components/ui/spinner"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { FolderGlyph } from "@/components/features/pages/folder-glyph"
import { cn } from "@/lib/utils"
import { moveImpactFromAcl, moveImpactFromShared } from "@/lib/pages/folder-sharing"
import {
  BULK_OUTCOME_LABEL,
  outcomeOf,
  shareConsequence,
  summariseBulk,
  type BulkItemResult,
} from "@/lib/pages/catalog-share"
import { useFolderAcl, usePageFolders } from "@/hooks/use-page-folders"
import { MoveError, movePage } from "@/lib/pages/catalog-move"

export interface ShareSubject {
  /** Immutable page ID: the move addresses it, never the slug. */
  id: string
  slug: string
  name: string
  pagesVersion: number | null
  /** Known version count, or null when the server did not say. */
  versions?: number | null
}

export interface ShareToFolderDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  subjects: ShareSubject[]
  /** Called once with the slugs that were moved, after a run that moved something. */
  onDone?: (moved: string[]) => void
}

export function ShareToFolderDialog({ workspaceId, open, onOpenChange, subjects, onDone }: ShareToFolderDialogProps) {
  const folders = usePageFolders(workspaceId, open)
  const [target, setTarget] = React.useState<string | null>(null)
  const [results, setResults] = React.useState<BulkItemResult[] | null>(null)
  const [running, setRunning] = React.useState(false)
  const identity = subjects.map(s => s.slug).join("\u0000")
  React.useEffect(() => {
    if (!open) return
    setTarget(null)
    setResults(null)
  }, [open, identity])

  const options = folders.folders.filter(f => f.canAcceptPages === true)
  const folder = options.find(f => f.slug === target) ?? null
  const acl = useFolderAcl(workspaceId, target, open && target !== null)
  // One source for text and request: a manager's ACL answer carries its own
  // version, and that is the one sent. The list's version is used only when
  // the ACL is not readable, where the text is built from the list marker too.
  const aclVersion = acl.manages ? acl.aclVersion : (folder?.aclVersion ?? null)
  const stale = Boolean(folder) && acl.manages && acl.aclVersion !== folder!.aclVersion
  const reread = folders.reread
  React.useEffect(() => { if (stale) void reread() }, [stale, reread])
  const gainsAccess: boolean | null = !folder || acl.loading ? null
    : acl.manages ? acl.entries.length > 0
      : folder.shared === "none" ? false : folder.shared === "unknown" ? null : true
  const audience = !folder || acl.loading ? null
    : acl.manages ? moveImpactFromAcl(acl.entries) : moveImpactFromShared(folder.shared)
  const many = subjects.length > 1
  const versions = many ? null : (subjects[0]?.versions ?? null)
  const consequence = folder && audience
    ? shareConsequence({
      name: many ? `${subjects.length} items` : subjects[0]?.name ?? "The item",
      targetName: folder.name,
      audience,
      versions,
      gainsAccess,
      each: many,
    })
    : null

  const confirm = async () => {
    if (!folder || !consequence || running || stale) return
    setRunning(true)
    const out: BulkItemResult[] = []
    for (const s of subjects) {
      try {
        await movePage(workspaceId, { id: s.id, targetFolder: folder.slug, pagesVersion: s.pagesVersion, aclVersion })
        out.push({ slug: s.slug, name: s.name, outcome: "success" })
      } catch (error) {
        const status = error instanceof MoveError ? error.status : undefined
        out.push({ slug: s.slug, name: s.name, outcome: outcomeOf({ status }), message: error instanceof Error ? error.message : undefined })
      }
    }
    setRunning(false)
    setResults(out)
    const moved = out.filter(r => r.outcome === "success").map(r => r.slug)
    if (moved.length) onDone?.(moved)
  }

  const tally = results ? summariseBulk(results) : null

  return (
    <CreateSurface open={open} onOpenChange={next => { if (!running) onOpenChange(next) }} size="sm" onSubmit={() => void confirm()}>
      <CreateSurfaceHeader
        concept="pages"
        context={many ? `${subjects.length} items` : subjects[0]?.name}
        title="Move to a working folder"
        description="Moving shares the item with the folder's audience. Check who gains it before you confirm."
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>
        {results ? (
          <div data-slot="share-results" className="flex flex-col gap-2">
            <p role="status" className="text-sm">
              {tally!.success} of {results.length} moved{results.length - tally!.success > 0 ? `; ${results.length - tally!.success} not moved` : ""}.
            </p>
            <ul aria-label="Result for each item" className="flex flex-col divide-y divide-border/40 rounded-md border border-border/50">
              {results.map(r => (
                <li key={r.slug} data-outcome={r.outcome} className="flex items-center gap-2 px-2.5 py-2 text-xs">
                  <span className="min-w-0 flex-1 truncate">{r.name}</span>
                  <span className={cn("shrink-0", r.outcome === "success" ? "text-muted-foreground" : "text-destructive")} title={r.message}>{BULK_OUTCOME_LABEL[r.outcome]}{r.message && r.outcome !== "success" ? `: ${r.message}` : ""}</span>
                </li>
              ))}
            </ul>
          </div>
        ) : folders.error ? (
          <p role="alert" className="text-destructive">The folders could not be read: {folders.error}</p>
        ) : folders.loading ? (
          <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground"><Spinner className="h-3.5 w-3.5" />Reading folders…</p>
        ) : options.length === 0 ? (
          <p role="status" className="text-sm text-muted-foreground">No working folder accepts items from you yet.</p>
        ) : (
          <>
            <div role="radiogroup" aria-label="Working folder" className="flex flex-col gap-1">
              {options.map(f => (
                <button
                  key={f.slug}
                  type="button"
                  role="radio"
                  aria-checked={target === f.slug}
                  data-folder-option={f.slug}
                  onClick={() => setTarget(f.slug)}
                  className={cn(
                    "flex min-h-8 items-center gap-2 rounded-md border px-2.5 py-1.5 text-left text-xs coarse:min-h-[44px]",
                    target === f.slug ? "border-primary/40 bg-primary/15 text-primary-hover" : "border-hairline bg-foreground/[0.03] text-muted-foreground",
                  )}
                >
                  <FolderGlyph icon={f.icon} color={f.color} />
                  <span className="min-w-0 flex-1 truncate">{f.name}</span>
                </button>
              ))}
            </div>
            {folder && (
              <div data-slot="share-consequence" className="mt-3 flex flex-col gap-1.5 rounded-md border border-border/50 bg-muted/20 px-3 py-2 text-xs leading-relaxed">
                {consequence ? (
                  <>
                    <p>{consequence.audience}</p>
                    <p>{consequence.history}</p>
                    <p>{consequence.identity}</p>
                    <p className="text-muted-foreground">{consequence.copy}</p>
                  </>
                ) : (
                  <p role="status" className="flex items-center gap-2 text-muted-foreground"><Spinner className="h-3 w-3" />Reading who will see it…</p>
                )}
                {stale && <p role="status" className="text-muted-foreground">The folder's permissions changed. Reading them again before you can confirm.</p>}
              </div>
            )}
          </>
        )}
      </CreateSurfaceBody>
      {results ? (
        <CreateSurfaceFooter onCancel={() => onOpenChange(false)} cancelLabel="Close" guardCancel={false} />
      ) : (
        <CreateSurfaceFooter
          onCancel={() => { if (!running) onOpenChange(false) }}
          primaryLabel="Move and share"
          onPrimary={() => void confirm()}
          primaryDisabled={!consequence || running || stale || subjects.length === 0}
          busy={running}
          guardCancel={false}
        />
      )}
    </CreateSurface>
  )
}
