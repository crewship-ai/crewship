"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import type { NativeVersion, NativeVersionList } from "@/lib/mooring/native-versions"

export interface NativeCraftVersionHistoryProps {
  /** Loads the listing under the caller's native admission. Null hides the panel (flag off). */
  load(signal: AbortSignal): Promise<NativeVersionList | null>
  /** A change reloads the listing, e.g. after a save or a restore. */
  refreshKey?: string | number
  /** True only for a person with Edit rights who holds the lease in this editor. */
  canRestore: boolean
  /** Why restore is unavailable, shown next to the list when canRestore is false. */
  restoreHint?: string
  pending?: boolean
  onRestore?(version: string): void
}

function when(value: string): string {
  const time = Date.parse(value)
  return Number.isFinite(time) ? new Date(time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : value
}
function author(version: NativeVersion): string {
  const name = version.author.displayName.trim() || (version.author.kind === "agent" ? "Agent" : "Uživatel")
  return version.author.kind === "agent" ? `${name} (agent)` : name
}

/**
 * Version history of a native Word or Sheet. Advisory UI only: listing and
 * restore are authorized by the server; restore is the existing fenced write
 * and appends a new version, so nothing in this list is ever removed.
 */
export function NativeCraftVersionHistory({ load, refreshKey, canRestore, restoreHint, pending = false, onRestore }: NativeCraftVersionHistoryProps) {
  const [list, setList] = React.useState<NativeVersionList | null>()
  const [failed, setFailed] = React.useState(false)
  const [open, setOpen] = React.useState(false)
  const [confirm, setConfirm] = React.useState<string>()
  const loader = React.useRef(load)
  loader.current = load
  React.useEffect(() => {
    const abort = new AbortController()
    setFailed(false)
    // A loader that throws synchronously is a failed load, never a failed render.
    void Promise.resolve().then(() => loader.current(abort.signal)).then(value => { if (!abort.signal.aborted) setList(value) }, () => { if (!abort.signal.aborted) { setList(undefined); setConfirm(undefined); setFailed(true) } })
    return () => abort.abort()
  }, [refreshKey])
  React.useEffect(() => { if (!canRestore) setConfirm(undefined) }, [canRestore])
  if (list === null) return null
  if (list === undefined && !failed) return null
  const count = list?.versions.length ?? 0
  return <section aria-label="Historie verzí" className="border-b px-3 py-2 text-sm">
    <Button type="button" size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen(v => !v)}>
      Historie verzí{list ? ` (${count}${list.truncated ? "+" : ""})` : ""}
    </Button>
    {open && failed && <p role="alert" className="py-1 text-muted-foreground">Historii verzí se nepodařilo načíst.</p>}
    {open && list && <>
      {!canRestore && restoreHint && <p className="py-1 text-muted-foreground">{restoreHint}</p>}
      <ol className="divide-y">
        {list.versions.map(version => <li key={version.version} aria-current={version.current ? "true" : undefined} className="flex flex-wrap items-center gap-2 py-1.5">
          <span className="min-w-0 flex-1">
            <b>Verze {version.version}</b>
            {version.current && <span className="ml-2 rounded border px-1 text-xs">aktuální</span>}
            <span className="block text-muted-foreground">{when(version.createdAt)} · {author(version)}{version.restoredFrom ? ` · obnovena z verze ${version.restoredFrom}` : ""}</span>
          </span>
          {!version.current && confirm !== version.version && <Button type="button" size="sm" variant="outline" disabled={!canRestore || pending} onClick={() => setConfirm(version.version)}>Obnovit</Button>}
          {!version.current && confirm === version.version && <span role="group" aria-label={`Obnovit verzi ${version.version}`} className="flex flex-wrap items-center gap-2">
            <span>Vznikne nová verze s obsahem verze {version.version}. Nic se nesmaže.</span>
            <Button type="button" size="sm" disabled={!canRestore || pending} onClick={() => { setConfirm(undefined); onRestore?.(version.version) }}>Obnovit jako novou verzi</Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setConfirm(undefined)}>Zrušit</Button>
          </span>}
        </li>)}
      </ol>
      {list.truncated && <p className="py-1 text-muted-foreground">Zobrazeno je posledních {count} verzí. Starší verze zůstávají uložené.</p>}
    </>}
  </section>
}
