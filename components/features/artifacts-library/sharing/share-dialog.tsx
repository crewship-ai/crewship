"use client"

import { useState } from "react"
import { Lock, Share2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { StatusPill } from "@/components/ui/status-pill"
import { useIssuePeople } from "@/hooks/use-issue-people"
import type { LibraryItem } from "../library-items"
import { accessVia, byAccess, LEVEL_TONE, LEVELS_DESC, lockedReason } from "./access-reason"
import {
  entryKey, SHARE_LEVEL_LABEL, SHARE_LEVEL_MEANING, useAccess, useSharingHuman, useSharingMutation,
  type AccessEntry, type ShareInput, type ShareLevel,
} from "./sharing-api"

const SELECT = "h-8 rounded-md border bg-transparent px-2 text-xs disabled:opacity-50"
const EVERYONE = "workspace:"

export interface ShareDialogProps {
  workspaceId: string
  item: LibraryItem
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** The kit's form surface (dialogs-through-kit guard): each change saves as it is made, so the footer only closes. */
export function ShareDialog({ workspaceId, item, open, onOpenChange }: ShareDialogProps) {
  // A new session or item starts a new form: nothing typed or read for the previous one carries over.
  const { humanId } = useSharingHuman()
  const close = () => onOpenChange(false)
  return <CreateSurface open={open} onOpenChange={onOpenChange} size="md">
    <CreateSurfaceHeader icon={Share2} title={`Share “${item.name}”`} description="Choose who in this workspace can open it, and how." onClose={close} />
    <CreateSurfaceBody>
      {open && <ShareBody key={`${humanId ?? ""}:${workspaceId}:${item.slug}`} workspaceId={workspaceId} item={item} />}
    </CreateSurfaceBody>
    <CreateSurfaceFooter hint={null} lead={<p className="mr-auto text-micro text-muted-foreground-soft">Only people in this workspace. No public links.</p>} onCancel={close} cancelLabel="Done" />
  </CreateSurface>
}

type RowChange = { entry: AccessEntry; level: ShareLevel | null }

function ShareBody({ workspaceId, item }: { workspaceId: string; item: LibraryItem }) {
  const { people } = useIssuePeople(workspaceId)
  const access = useAccess(workspaceId, item)
  const [subject, setSubject] = useState("")
  const [level, setLevel] = useState<ShareLevel>("read")
  const add = useSharingMutation<ShareInput, void>(workspaceId, (api, input) => api.share(item, input), item.slug)
  const change = useSharingMutation<RowChange, void>(workspaceId, (api, { entry, level }) => level
    ? api.share(item, { subjectType: entry.subjectType, subjectId: entry.subjectId, label: entry.label, level })
    : api.revoke(item, entry.subjectType, entry.subjectId), item.slug)

  const entries = [...(access.data ?? [])].sort(byAccess)
  const busyKey = change.isPending && change.variables ? entryKey(change.variables.entry) : null

  const submit = () => {
    if (!subject || add.isPending) return
    const person = people.find(p => `user:${p.id}` === subject)
    const input: ShareInput = subject === EVERYONE
      ? { subjectType: "workspace", subjectId: "", label: "Everyone in this workspace", level }
      : { subjectType: "user", subjectId: person?.id ?? subject.slice(5), label: person?.name ?? subject.slice(5), level }
    add.mutate(input, { onSuccess: () => setSubject("") })
  }

  return <div className="flex min-w-0 flex-col gap-4">
    <form className="flex flex-col gap-1.5" onSubmit={e => { e.preventDefault(); submit() }}>
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Share with" className={`${SELECT} min-w-0 flex-1`} value={subject} disabled={add.isPending} onChange={e => setSubject(e.target.value)}>
          <option value="">Add a person…</option>
          <option value={EVERYONE}>Everyone in this workspace</option>
          {people.map(p => <option key={p.id} value={`user:${p.id}`}>{p.name}</option>)}
        </select>
        <select aria-label="Level" className={SELECT} value={level} disabled={add.isPending} onChange={e => setLevel(e.target.value as ShareLevel)}>
          {LEVELS_DESC.map(l => <option key={l} value={l}>{SHARE_LEVEL_LABEL[l]}</option>)}
        </select>
        <Button type="submit" size="sm" className="h-8 text-xs" disabled={!subject || add.isPending}>{add.isPending ? "Sharing…" : "Share"}</Button>
      </div>
      <p className="text-micro text-muted-foreground">{SHARE_LEVEL_LABEL[level]}: {SHARE_LEVEL_MEANING[level]}</p>
      {add.error && <p role="alert" className="text-xs text-destructive">{add.error.message}</p>}
    </form>

    <section aria-label="People with access" className="flex flex-col gap-1">
      <h3 className="text-xs font-semibold text-muted-foreground">Who has access</h3>
      {access.isPending ? <p role="status" className="text-xs text-muted-foreground">Loading access…</p>
        : access.error ? <p role="alert" className="text-xs text-destructive">{access.error.message}</p>
        : entries.length === 0 ? <p className="text-xs text-muted-foreground">Nobody yet.</p>
        : <ul className="flex flex-col divide-y">
          {entries.map(entry => {
            const key = entryKey(entry)
            const locked = lockedReason(entry)
            const busy = busyKey === key
            return <li key={`${key}:${entry.via}:${entry.folderSlug ?? ""}`} data-entry={key} aria-busy={busy || undefined} className="flex flex-wrap items-center gap-2 py-2">
              <div className="min-w-0 flex-1">
                <p className="truncate text-xs font-medium">{entry.label}</p>
                <p className="text-micro text-muted-foreground">{accessVia(entry)}{locked && entry.via !== "owner" ? ` · ${locked}` : ""}</p>
              </div>
              {locked ? <span title={locked} className="flex items-center gap-1.5">
                <Lock aria-hidden className="size-3 text-muted-foreground" />
                <StatusPill tone={LEVEL_TONE[entry.level]} label={SHARE_LEVEL_LABEL[entry.level]} />
              </span> : <>
                <select aria-label={`Level for ${entry.label}`} className={SELECT} value={busy && change.variables?.level ? change.variables.level : entry.level} disabled={change.isPending}
                  onChange={e => change.mutate({ entry, level: e.target.value as ShareLevel })}>
                  {LEVELS_DESC.map(l => <option key={l} value={l}>{SHARE_LEVEL_LABEL[l]}</option>)}
                </select>
                <Button type="button" size="sm" variant="ghost" className="h-8 text-xs" aria-label={`Remove ${entry.label}`} disabled={change.isPending}
                  onClick={() => change.mutate({ entry, level: null })}>{busy && change.variables?.level === null ? "Removing…" : "Remove"}</Button>
              </>}
            </li>
          })}
        </ul>}
      {change.error && <p role="alert" className="text-xs text-destructive">{change.error.message}</p>}
    </section>
  </div>
}
