"use client"

import { useEffect, useMemo, useState } from "react"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { StatusPill } from "@/components/ui/status-pill"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import type { LibraryItem } from "../library-items"
import { LevelSelect, SELECT, SubjectSelect, useSubjects, type Subject } from "./share-controls"
import {
  BULK_LIMIT, SHARE_LEVEL_LABEL, useSharingApi, useSharingMutation,
  type AccessEntry, type BulkAction, type BulkRequest, type BulkResult, type ShareLevel,
} from "./sharing-api"

export type FolderRef = { slug: string; name: string }

export interface BulkBarProps {
  workspaceId: string
  selected: LibraryItem[]
  folders: FolderRef[]
  /** The Archive view offers Unarchive instead of Archive. */
  archivedView?: boolean
  onClear: () => void
  onDone: (result: BulkResult) => void
}

/** What is selected and what can be done to all of it; every action previews first. */
export function BulkBar({ workspaceId, selected, folders, archivedView = false, onClear, onDone }: BulkBarProps) {
  const [action, setAction] = useState<BulkAction | null>(null)
  if (selected.length === 0) return null
  return <div role="toolbar" aria-label="Selection" className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-card px-2.5 py-1.5 text-xs">
    <span className="mr-auto tabular-nums">{selected.length} selected</span>
    <Button size="xs" variant="outline" onClick={() => setAction("move")}>Move…</Button>
    <Button size="xs" variant="outline" onClick={() => setAction("share")}>Share…</Button>
    {archivedView
      ? <Button size="xs" variant="outline" onClick={() => setAction("unarchive")}>Unarchive</Button>
      : <Button size="xs" variant="outline" onClick={() => setAction("archive")}>Archive</Button>}
    <Button size="xs" variant="ghost" onClick={onClear}>Clear</Button>
    {action && <BulkPreviewDialog workspaceId={workspaceId} action={action} items={selected} folders={folders} open
      onOpenChange={open => { if (!open) setAction(null) }} onDone={onDone} />}
  </div>
}

export interface MoveToFolderDialogProps {
  workspaceId: string
  item: LibraryItem
  folders: FolderRef[]
  open: boolean
  onOpenChange: (open: boolean) => void
  onDone?: (result: BulkResult) => void
}

/** Moving one artifact is the bulk preview with one item. */
export function MoveToFolderDialog({ item, ...rest }: MoveToFolderDialogProps) {
  return <BulkPreviewDialog {...rest} action="move" items={[item]} />
}

export interface BulkPreviewDialogProps {
  workspaceId: string
  action: BulkAction
  items: LibraryItem[]
  folders: FolderRef[]
  open: boolean
  onOpenChange: (open: boolean) => void
  onDone?: (result: BulkResult) => void
}

const TITLE: Record<BulkAction, string> = { move: "Move", share: "Share", archive: "Archive", unarchive: "Unarchive" }
const WILL: Record<BulkAction, string> = { move: "will move", share: "will be shared", archive: "will be archived", unarchive: "will be restored" }
const DID: Record<BulkAction, string> = { move: "moved", share: "shared", archive: "archived", unarchive: "restored" }
const UNFILED = "__unfiled"

const listEntries = (entries: AccessEntry[]) => entries.map(e => `${e.label} (${SHARE_LEVEL_LABEL[e.level]})`).join(", ")

/** Changes 26–27: pick the action, read its dry run per item, confirm only what can happen. */
export function BulkPreviewDialog({ workspaceId, action, items, folders, open, onOpenChange, onDone }: BulkPreviewDialogProps) {
  const api = useSharingApi(workspaceId)
  const { subjects } = useSubjects(workspaceId, open && action === "share")
  const [target, setTarget] = useState("")
  const [subject, setSubject] = useState<Subject | null>(null)
  const [level, setLevel] = useState<ShareLevel>("read")
  const [preview, setPreview] = useState<BulkResult | null>(null)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [applied, setApplied] = useState<BulkResult | null>(null)
  const apply = useSharingMutation(workspaceId, (sharing, request: BulkRequest) => sharing.bulk(request))
  const tooMany = items.length > BULK_LIMIT
  const identity = items.map(i => i.slug).join("\u0000")

  useEffect(() => {
    if (!open) return
    setTarget(""); setSubject(null); setLevel("read"); setPreview(null); setPreviewError(null); setApplied(null)
  }, [open, action, identity])

  /** The request without its items and dry-run flag, or null while the action still needs a choice. */
  const base = useMemo((): Omit<BulkRequest, "items" | "dryRun"> | null => {
    if (action === "move") return target ? { action, folder: target === UNFILED ? null : folders.find(f => f.slug === target) ?? null } : null
    if (action === "share") return subject ? { action, subjectType: subject.subjectType, subjectId: subject.subjectId, subjectLabel: subject.label, level } : null
    return { action }
    // Folders are read by slug, so a parent re-creating the list does not re-run the preview.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [action, target, subject, level])

  useEffect(() => {
    if (!open || !base || tooMany) return
    let live = true
    setPreview(null); setPreviewError(null)
    api.bulk({ ...base, items, dryRun: true })
      .then(result => { if (live) setPreview(result) })
      .catch((error: unknown) => { if (live) setPreviewError(error instanceof Error ? error.message : "The preview failed.") })
    return () => { live = false }
    // `items` is tracked by identity: a new array with the same slugs is the same selection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, open, base, tooMany, identity])

  const ok = preview?.items.filter(r => r.ok) ?? []
  const skipped = (preview?.items.length ?? 0) - ok.length
  const nameOf = (slug: string) => items.find(i => i.slug === slug)?.name ?? slug

  const confirm = () => {
    if (!base || !ok.length) return
    const slugs = new Set(ok.map(r => r.slug))
    apply.mutate({ ...base, items: items.filter(i => slugs.has(i.slug)), dryRun: false }, {
      onSuccess: result => {
        onDone?.(result)
        if (result.items.every(r => r.ok)) onOpenChange(false)
        else setApplied(result)
      },
    })
  }

  const count = items.length === 1 ? items[0].name : `${items.length} artifacts`
  const description = action === "archive" ? "Archived artifacts are hidden, not deleted. You can restore them from Archive."
    : action === "unarchive" ? "Restored artifacts return to the library where they were."
    : action === "move" ? "Moving changes who can open an artifact to match the folder. Check the preview before you confirm."
    : "Shares within this workspace. Check the preview before you confirm."

  return <Dialog open={open} onOpenChange={next => { if (!apply.isPending) onOpenChange(next) }}>
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{TITLE[action]} {count}</DialogTitle>
        <DialogDescription>{description}</DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-3 text-xs">
        {tooMany ? <p role="alert" className="text-destructive">{items.length} selected. At most {BULK_LIMIT} artifacts at once; select fewer.</p>
          : applied ? <Outcome action={action} result={applied} nameOf={nameOf} />
          : <>
            {action === "move" && <label className="flex items-center gap-2 text-muted-foreground">To
              <select aria-label="Folder" className={SELECT} value={target} onChange={event => setTarget(event.target.value)}>
                <option value="">Choose a folder…</option>
                <option value={UNFILED}>Unfiled</option>
                {folders.map(f => <option key={f.slug} value={f.slug}>{f.name}</option>)}
              </select>
            </label>}
            {action === "share" && <div className="flex flex-wrap items-center gap-2">
              <SubjectSelect label="Share with" subjects={subjects} value={subject} onChange={setSubject} />
              <LevelSelect value={level} onChange={setLevel} />
            </div>}
            {previewError ? <p role="alert" className="text-destructive">{previewError}</p>
              : base && !preview ? <p role="status" className="flex items-center gap-2 text-muted-foreground"><Spinner className="size-3.5" />Checking what would change…</p>
              : preview && <>
                <p role="status" data-slot="bulk-totals" className="font-medium">{ok.length} {WILL[action]}{skipped ? `, ${skipped} skipped` : ""}</p>
                <ul aria-label="Impact for each artifact" className="flex flex-col divide-y divide-border/40 rounded-md border border-border/50">
                  {preview.items.map(r => <li key={r.slug} data-ok={r.ok} className="flex flex-col gap-0.5 px-2.5 py-1.5">
                    <span className="flex items-center gap-2">
                      <span className="min-w-0 flex-1 truncate">{nameOf(r.slug)}</span>
                      {!r.ok && <StatusPill tone="warn" label="Skipped" />}
                    </span>
                    {r.ok
                      ? <ImpactLines action={action} gains={r.gains} loses={r.loses} />
                      : <span className="text-muted-foreground">{r.reason ?? "Not allowed."}</span>}
                  </li>)}
                </ul>
              </>}
            {apply.error && <p role="alert" className="text-destructive">Nothing was confirmed: {apply.error.message}</p>}
          </>}
      </div>
      <DialogFooter>
        {applied || tooMany
          ? <Button size="sm" variant="outline" onClick={() => onOpenChange(false)}>Close</Button>
          : <>
            <Button size="sm" variant="outline" onClick={() => onOpenChange(false)} disabled={apply.isPending}>Cancel</Button>
            <Button size="sm" onClick={confirm} disabled={!ok.length || apply.isPending}>
              {apply.isPending ? "Applying…" : `${TITLE[action]} ${ok.length || ""}`.trim()}
            </Button>
          </>}
      </DialogFooter>
    </DialogContent>
  </Dialog>
}

function ImpactLines({ action, gains, loses }: { action: BulkAction; gains: AccessEntry[]; loses: AccessEntry[] }) {
  if (action === "archive") return <span className="text-muted-foreground">Hidden from the library, not deleted.</span>
  if (action === "unarchive") return <span className="text-muted-foreground">Back in the library.</span>
  if (!gains.length && !loses.length) return <span className="text-muted-foreground">No change in who can open it.</span>
  return <>
    {gains.length > 0 && <span>Gains: {listEntries(gains)}.</span>}
    {loses.length > 0 && <span>Loses: {listEntries(loses)}.</span>}
  </>
}

function Outcome({ action, result, nameOf }: { action: BulkAction; result: BulkResult; nameOf: (slug: string) => string }) {
  const failed = result.items.filter(r => !r.ok)
  return <>
    <p role="status" className="font-medium">{result.items.length - failed.length} {DID[action]}, {failed.length} not</p>
    <ul aria-label="Not applied" className="flex flex-col divide-y divide-border/40 rounded-md border border-border/50">
      {failed.map(r => <li key={r.slug} className="flex items-center gap-2 px-2.5 py-1.5">
        <span className="min-w-0 flex-1 truncate">{nameOf(r.slug)}</span>
        <span className="text-muted-foreground">{r.reason ?? "Not allowed."}</span>
      </li>)}
    </ul>
  </>
}
