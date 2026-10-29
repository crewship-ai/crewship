"use client"

import { Share2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import type { LibraryItem } from "../library-items"
import { accessVia, byAccess, LEVEL_TONE, LEVELS_DESC, lockedReason } from "./access-reason"
import { entryKey, SHARE_LEVEL_LABEL, SHARE_LEVEL_MEANING, SharingError, useAccess } from "./sharing-api"

export interface AccessTabProps {
  workspaceId: string
  item: LibraryItem
  onShare: () => void
}

export function AccessTab({ workspaceId, item, onShare }: AccessTabProps) {
  const access = useAccess(workspaceId, item)
  const entries = [...(access.data ?? [])].sort(byAccess)
  const groups = LEVELS_DESC.map(level => ({ level, entries: entries.filter(e => e.level === level) })).filter(g => g.entries.length > 0)

  return <section aria-label="Access" className="flex min-w-0 flex-col gap-3 p-3">
    <div className="flex items-center gap-2">
      <div className="min-w-0 flex-1">
        <h3 className="text-xs font-semibold">Who can open it</h3>
        {access.data && <p className="text-micro text-muted-foreground">
          {entries.length === 1 ? "1 person or group" : `${entries.length} people and groups`}
          {groups.length > 0 && ` · ${groups.map(g => `${g.entries.length} ${SHARE_LEVEL_LABEL[g.level]}`).join(", ")}`}
        </p>}
      </div>
      <Button type="button" size="sm" variant="secondary" className="h-7 text-xs" onClick={onShare}><Share2 aria-hidden className="size-3.5" />Share…</Button>
    </div>

    {access.isPending ? <p role="status" className="text-xs text-muted-foreground">Loading access…</p>
      : access.error ? <p role="alert" className="text-xs text-muted-foreground">
        {access.error instanceof SharingError && access.error.status === 403 ? "Only people who can share this artifact see its access list." : access.error.message}
      </p>
      : groups.length === 0 ? <p className="text-xs text-muted-foreground">Nobody else can open it yet.</p>
      : groups.map(({ level, entries }) => <div key={level} role="group" aria-label={`${SHARE_LEVEL_LABEL[level]} access`} data-level={level} className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <StatusPill tone={LEVEL_TONE[level]} label={SHARE_LEVEL_LABEL[level]} />
          <span className="text-micro tabular-nums text-muted-foreground">{entries.length}</span>
        </div>
        <p className="text-micro text-muted-foreground-soft">{SHARE_LEVEL_MEANING[level]}</p>
        <ul className="flex flex-col divide-y">
          {entries.map(entry => {
            const locked = lockedReason(entry)
            return <li key={`${entryKey(entry)}:${entry.via}:${entry.folderSlug ?? ""}`} className="flex items-baseline gap-2 py-1.5">
              <span className="min-w-0 flex-1 truncate text-xs">{entry.label}</span>
              <span className="shrink-0 text-micro text-muted-foreground">{accessVia(entry)}{entry.via === "folder" && locked ? ` · ${locked}` : ""}</span>
            </li>
          })}
        </ul>
      </div>)}
  </section>
}
