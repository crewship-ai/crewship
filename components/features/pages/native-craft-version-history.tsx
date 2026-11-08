"use client"

import * as React from "react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
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
  /** Inside a drawer tab (the Artifacts frame): always open, no toggle row, entries in plain language. */
  embedded?: boolean
}

function when(value: string): string {
  const time = Date.parse(value)
  return Number.isFinite(time) ? new Date(time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : value
}
function author(version: NativeVersion): string {
  const name = version.author.displayName.trim() || (version.author.kind === "agent" ? "Agent" : "User")
  return version.author.kind === "agent" ? `${name} (agent)` : name
}

/** One history entry as a sentence: who saved it, or which version it brought back. */
export function plainEntry(version: NativeVersion): string {
  return version.restoredFrom ? `${author(version)} restored version ${version.restoredFrom}` : `Saved by ${author(version)}`
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
export function NativeCraftVersionHistory({ load, loadVersion, Preview, scope, refreshKey, canRestore, restoreHint, pending = false, onRestore, embedded = false }: NativeCraftVersionHistoryProps) {
  const [list, setList] = React.useState<NativeVersionList | null>()
  const [failed, setFailed] = React.useState(false)
  const [expanded, setOpen] = React.useState(false)
  const open = embedded || expanded
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
    }, () => { if (!abort.signal.aborted) { setComparison(undefined); setPanelError("The version could not be loaded for comparison. Nothing changed.") } })
    return () => abort.abort()
  }, [panel, list])
  if (list === null) return null
  if (list === undefined && !failed) return null
  const count = list?.versions.length ?? 0
  const show = (kind: Panel["kind"], version: string, event: React.MouseEvent<HTMLElement>) => { setComparison(undefined); setPanelError(undefined); setPanel({ kind, version, opener: event.currentTarget }) }
  const keys = (event: React.KeyboardEvent) => { if (event.key === "Escape") { event.stopPropagation(); closePanel() } }
  const read = panel && loadVersion ? (signal: AbortSignal) => loadVersion(panel.version, signal) : undefined
  const PreviewView = Preview ?? NativeCraftVersionFramePreview
  return <section aria-label="Version history" className={embedded ? "grid content-start gap-1 p-3.5 text-sm" : "border-b px-3 py-2 text-sm"}>
    {!embedded && <Button type="button" size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen(v => !v)}>
      Version history{list ? ` (${count}${list.truncated ? "+" : ""})` : ""}
    </Button>}
    {open && failed && <p role="alert" className="py-1 text-muted-foreground">The version history could not be loaded.</p>}
    {open && list && <>
      {!canRestore && restoreHint && <p className="py-1 text-muted-foreground">{restoreHint}</p>}
      <ol className="divide-y">
        {list.versions.map(version => <li key={version.version} aria-current={version.current ? "true" : undefined} className="flex flex-wrap items-center gap-2 py-1.5">
          {embedded ? <span className="flex min-w-0 flex-[1_1_100%] items-start gap-2">
            <span className="grid min-w-0 flex-1">
              <b className="font-medium">{plainEntry(version)}</b>
              <small className="text-xs text-muted-foreground">{when(version.createdAt)}</small>
            </span>
            <span className="shrink-0 font-mono text-xs text-muted-foreground">v{version.version}</span>
            {version.current && <StatusPill tone="blue" label="Latest" />}
          </span> : <span className="min-w-0 flex-1">
            <b>Version {version.version}</b>
            {version.current && <span className="ml-2 rounded border px-1 text-xs">current</span>}
            <span className="block text-muted-foreground">{when(version.createdAt)} · {author(version)}{version.restoredFrom ? ` · restored from version ${version.restoredFrom}` : ""}</span>
          </span>}
          {loadVersion && <Button type="button" size="sm" variant="ghost" aria-label={`Preview of version ${version.version}`} aria-pressed={panel?.kind === "preview" && panel.version === version.version} onClick={event => show("preview", version.version, event)}>Preview</Button>}
          {loadVersion && !version.current && <Button type="button" size="sm" variant="ghost" aria-label={`Compare version ${version.version} with the current one`} aria-pressed={panel?.kind === "compare" && panel.version === version.version} onClick={event => show("compare", version.version, event)}>Compare</Button>}
          {!version.current && confirm !== version.version && <Button type="button" size="sm" variant="outline" disabled={!canRestore || pending} onClick={() => setConfirm(version.version)}>Restore</Button>}
          {!version.current && confirm === version.version && <span role="group" aria-label={`Restore version ${version.version}`} className="flex flex-wrap items-center gap-2">
            <span>This adds a new version with the content of version {version.version}. Nothing is deleted.</span>
            <Button type="button" size="sm" disabled={!canRestore || pending} onClick={() => { setConfirm(undefined); onRestore?.(version.version) }}>Restore as a new version</Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setConfirm(undefined)}>Cancel</Button>
          </span>}
        </li>)}
      </ol>
      {list.truncated && <p className="py-1 text-muted-foreground">Showing the last {count} versions. Older versions stay saved.</p>}
      {embedded && <p className="py-1 text-xs text-muted-foreground">A restore always adds a new version. Nothing is deleted.</p>}
      {panel && read && <div role="region" aria-labelledby="native-version-panel-heading" className="mt-2 space-y-2 border-t pt-2" onKeyDown={keys}>
        <div className="flex flex-wrap items-center gap-2">
          <h3 id="native-version-panel-heading" ref={heading} tabIndex={-1} className="flex-1 font-medium outline-none">
            {panel.kind === "preview" ? `Preview of version ${panel.version} (read-only)` : `Comparison of version ${panel.version} with the current version ${list.currentVersion}`}
          </h3>
          <Button type="button" size="sm" variant="outline" onClick={closePanel}>Close</Button>
        </div>
        {panel.kind === "preview" && <div className="h-[420px]"><PreviewView key={panel.version} version={panel.version} read={read} {...(scope ? { scope } : {})} /></div>}
        {panel.kind === "compare" && panelError && <p role="alert">{panelError}</p>}
        {panel.kind === "compare" && !panelError && !comparison && <p role="status">Loading the comparison…</p>}
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
      {comparison.equal ? `Version ${comparison.version} has the same content as the current version ${comparison.current}.`
        : comparison.lines ? `Compared with version ${comparison.version}, current version ${comparison.current} has ${added} added and ${removed} removed lines.`
          : `Version ${comparison.version} and the current version ${comparison.current} differ; the document is too large for a line comparison.`}
    </p>
    {comparison.lines && <ol aria-label="Line differences" className="max-h-72 overflow-auto rounded border font-mono text-xs">
      {comparison.lines.filter(l => l.kind !== "same").map((line, i) => <li key={i} className={line.kind === "added" ? "bg-success/10" : "bg-destructive/10"}>
        <span aria-hidden="true">{line.kind === "added" ? "+ " : "− "}</span><span className="sr-only">{line.kind === "added" ? "added: " : "removed: "}</span>{line.text}
      </li>)}
    </ol>}
    <div className="grid gap-2 md:grid-cols-2">
      <label>Version {comparison.version}<textarea aria-label={`Content of version ${comparison.version}`} readOnly value={comparison.before} className="h-40 w-full font-mono text-xs" /></label>
      <label>Current version {comparison.current}<textarea aria-label={`Content of the current version ${comparison.current}`} readOnly value={comparison.after} className="h-40 w-full font-mono text-xs" /></label>
    </div>
  </div>
}
