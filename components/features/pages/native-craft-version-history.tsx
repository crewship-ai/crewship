"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { nativeLineDiff, nativeVersionText, type NativeDiffLine, type NativeVersion, type NativeVersionDocument, type NativeVersionList } from "@/lib/mooring/native-versions"
import { NativeCraftVersionFramePreview } from "./native-craft-version-preview"

export interface NativeCraftVersionPreviewProps {
  version: string
  /** Re-reads the version under the caller's current authority on every recheck. */
  read(signal: AbortSignal): Promise<NativeVersionDocument>
  scope?: { humanId: string; workspaceId: string; pageId: string }
}

export interface NativeCraftVersionHistoryProps {
  /** Loads the listing under the caller's native admission. Null hides the panel (flag off). */
  load(signal: AbortSignal): Promise<NativeVersionList | null>
  /** Reads one saved version's bytes, read-only, for preview and comparison. Without it neither is offered. */
  loadVersion?(version: string, signal: AbortSignal): Promise<NativeVersionDocument>
  /** Renders a read-only preview; the default is the isolated native editor frame, in read-only mode. */
  Preview?: React.ComponentType<NativeCraftVersionPreviewProps>
  /** Identity and scope of the default preview frame. */
  scope?: { humanId: string; workspaceId: string; pageId: string }
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

interface Comparison {
  version: string
  current: string
  equal: boolean
  before: string
  after: string
  lines: NativeDiffLine[] | null
}

type Panel = { kind: "preview" | "compare"; version: string; opener: HTMLElement | null }

/**
 * Version history of a native Word or Sheet. Advisory UI only: listing,
 * version reads and restore are authorized by the server; restore is the
 * existing fenced write and appends a new version, so nothing in this list is
 * ever removed. Preview and comparison are read-only and disappear with the
 * list when a reload fails (for example after access is revoked).
 */
export function NativeCraftVersionHistory({ load, loadVersion, Preview, scope, refreshKey, canRestore, restoreHint, pending = false, onRestore }: NativeCraftVersionHistoryProps) {
  const [list, setList] = React.useState<NativeVersionList | null>()
  const [failed, setFailed] = React.useState(false)
  const [open, setOpen] = React.useState(false)
  const [confirm, setConfirm] = React.useState<string>()
  const [panel, setPanel] = React.useState<Panel>()
  const [comparison, setComparison] = React.useState<Comparison>()
  const [panelError, setPanelError] = React.useState<string>()
  const loader = React.useRef(load)
  loader.current = load
  const versionLoader = React.useRef(loadVersion)
  versionLoader.current = loadVersion
  const heading = React.useRef<HTMLHeadingElement>(null)
  const closePanel = React.useCallback(() => {
    setPanel(current => {
      const opener = current?.opener
      if (opener) queueMicrotask(() => { if (opener.isConnected) opener.focus() })
      return undefined
    })
    setComparison(undefined)
    setPanelError(undefined)
  }, [])
  React.useEffect(() => {
    const abort = new AbortController()
    setFailed(false)
    // A loader that throws synchronously is a failed load, never a failed render.
    void Promise.resolve().then(() => loader.current(abort.signal)).then(value => {
      if (abort.signal.aborted) return
      setList(value)
      if (!value) { setPanel(undefined); setComparison(undefined) }
    }, () => { if (!abort.signal.aborted) { setList(undefined); setConfirm(undefined); setPanel(undefined); setComparison(undefined); setPanelError(undefined); setFailed(true) } })
    return () => abort.abort()
  }, [refreshKey])
  React.useEffect(() => { if (!canRestore) setConfirm(undefined) }, [canRestore])
  // A version that left the listing (or a new head) closes its panel.
  React.useEffect(() => {
    if (panel && list && !list.versions.some(v => v.version === panel.version)) { setPanel(undefined); setComparison(undefined) }
    if (comparison && list && comparison.current !== list.currentVersion) setComparison(undefined)
  }, [list, panel, comparison])
  React.useEffect(() => { if (panel) heading.current?.focus() }, [panel])
  React.useEffect(() => {
    if (panel?.kind !== "compare" || !list) return
    const read = versionLoader.current
    if (!read) return
    const abort = new AbortController()
    void Promise.all([read(panel.version, abort.signal), read(list.currentVersion, abort.signal)]).then(([old, head]) => {
      if (abort.signal.aborted) return
      const before = nativeVersionText(old.bytes), after = nativeVersionText(head.bytes)
      const equal = old.bytes.length === head.bytes.length && old.bytes.every((b, i) => b === head.bytes[i])
      setComparison({ version: old.version, current: head.version, equal, before, after, lines: equal ? null : nativeLineDiff(before, after) })
    }, () => { if (!abort.signal.aborted) { setComparison(undefined); setPanelError("Verzi se nepodařilo načíst k porovnání. Nic se nezměnilo.") } })
    return () => abort.abort()
  }, [panel, list])
  if (list === null) return null
  if (list === undefined && !failed) return null
  const count = list?.versions.length ?? 0
  const show = (kind: Panel["kind"], version: string, event: React.MouseEvent<HTMLElement>) => { setComparison(undefined); setPanelError(undefined); setPanel({ kind, version, opener: event.currentTarget }) }
  const keys = (event: React.KeyboardEvent) => { if (event.key === "Escape") { event.stopPropagation(); closePanel() } }
  const read = panel && loadVersion ? (signal: AbortSignal) => loadVersion(panel.version, signal) : undefined
  const PreviewView = Preview ?? NativeCraftVersionFramePreview
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
          {loadVersion && <Button type="button" size="sm" variant="ghost" aria-label={`Náhled verze ${version.version}`} aria-pressed={panel?.kind === "preview" && panel.version === version.version} onClick={event => show("preview", version.version, event)}>Náhled</Button>}
          {loadVersion && !version.current && <Button type="button" size="sm" variant="ghost" aria-label={`Porovnat verzi ${version.version} s aktuální`} aria-pressed={panel?.kind === "compare" && panel.version === version.version} onClick={event => show("compare", version.version, event)}>Porovnat</Button>}
          {!version.current && confirm !== version.version && <Button type="button" size="sm" variant="outline" disabled={!canRestore || pending} onClick={() => setConfirm(version.version)}>Obnovit</Button>}
          {!version.current && confirm === version.version && <span role="group" aria-label={`Obnovit verzi ${version.version}`} className="flex flex-wrap items-center gap-2">
            <span>Vznikne nová verze s obsahem verze {version.version}. Nic se nesmaže.</span>
            <Button type="button" size="sm" disabled={!canRestore || pending} onClick={() => { setConfirm(undefined); onRestore?.(version.version) }}>Obnovit jako novou verzi</Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setConfirm(undefined)}>Zrušit</Button>
          </span>}
        </li>)}
      </ol>
      {list.truncated && <p className="py-1 text-muted-foreground">Zobrazeno je posledních {count} verzí. Starší verze zůstávají uložené.</p>}
      {panel && read && <div role="region" aria-labelledby="native-version-panel-heading" className="mt-2 space-y-2 border-t pt-2" onKeyDown={keys}>
        <div className="flex flex-wrap items-center gap-2">
          <h3 id="native-version-panel-heading" ref={heading} tabIndex={-1} className="flex-1 font-medium outline-none">
            {panel.kind === "preview" ? `Náhled verze ${panel.version} (pouze pro čtení)` : `Porovnání verze ${panel.version} s aktuální verzí ${list.currentVersion}`}
          </h3>
          <Button type="button" size="sm" variant="outline" onClick={closePanel}>Zavřít</Button>
        </div>
        {panel.kind === "preview" && <div className="h-[420px]"><PreviewView key={panel.version} version={panel.version} read={read} {...(scope ? { scope } : {})} /></div>}
        {panel.kind === "compare" && panelError && <p role="alert">{panelError}</p>}
        {panel.kind === "compare" && !panelError && !comparison && <p role="status">Načítám porovnání…</p>}
        {panel.kind === "compare" && comparison && <ComparisonView comparison={comparison} />}
      </div>}
    </>}
  </section>

}

function ComparisonView({ comparison }: { comparison: Comparison }) {
  const added = comparison.lines?.filter(l => l.kind === "added").length ?? 0
  const removed = comparison.lines?.filter(l => l.kind === "removed").length ?? 0
  return <div className="space-y-2">
    <p role="status">
      {comparison.equal ? `Verze ${comparison.version} má stejný obsah jako aktuální verze ${comparison.current}.`
        : comparison.lines ? `Oproti verzi ${comparison.version} má aktuální verze ${comparison.current} ${added} přidaných a ${removed} odebraných řádků.`
          : `Verze ${comparison.version} a aktuální verze ${comparison.current} se liší; dokument je pro řádkové porovnání příliš velký.`}
    </p>
    {comparison.lines && <ol aria-label="Rozdíly po řádcích" className="max-h-72 overflow-auto rounded border font-mono text-xs">
      {comparison.lines.filter(l => l.kind !== "same").map((line, i) => <li key={i} className={line.kind === "added" ? "bg-success/10" : "bg-destructive/10"}>
        <span aria-hidden="true">{line.kind === "added" ? "+ " : "− "}</span><span className="sr-only">{line.kind === "added" ? "přidáno: " : "odebráno: "}</span>{line.text}
      </li>)}
    </ol>}
    <div className="grid gap-2 md:grid-cols-2">
      <label>Verze {comparison.version}<textarea aria-label={`Obsah verze ${comparison.version}`} readOnly value={comparison.before} className="h-40 w-full font-mono text-xs" /></label>
      <label>Aktuální verze {comparison.current}<textarea aria-label={`Obsah aktuální verze ${comparison.current}`} readOnly value={comparison.after} className="h-40 w-full font-mono text-xs" /></label>
    </div>
  </div>
}
