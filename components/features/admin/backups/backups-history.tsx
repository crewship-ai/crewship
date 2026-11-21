"use client"

import * as React from "react"
import { Download, History, Pin, PinOff, X } from "lucide-react"

import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { useShowMore } from "@/hooks/use-show-more"
import { ShowMore } from "@/components/ui/show-more"
import { StatusPill } from "@/components/ui/status-pill"
import {
  SettingsCard, SettingsRow, SettingsSegmented, nativeSelect, selectionBar, selectionBarAction, selectionBarButton,
} from "@/components/features/settings/shared"
import { BackupsTable, Chip, Gate, ProofSteps, WsName, type TableColumn } from "./backups-kit"
import {
  TRIGGER_LABEL, formatHold, formatSize, formatWhen, runDuration, runPlanLabel, runResult, runsCapNote, type BackupRun, type RunPhase,
} from "./backups-model"
import { checkBundle, downloadHref, pinBundle, useBackupRuns, workspaceFor } from "./use-backup-runs"
import { perform, type Resource } from "./use-backups-data"
import { ManifestDialog } from "./backups-run-dialogs"
import type { RunFilter, RunQuery, SectionCtx } from "./backups-scope"

const FILTERS: { value: RunFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "failed", label: "Failed" },
  { value: "incomplete", label: "Incomplete" },
  { value: "manual", label: "Manual" },
  { value: "pinned", label: "Pinned" },
]

/** The Runs facets, the same test the side panel counts with. */
/** The runs the current scope shows. The side panel counts with it too. */
export function runsInScope(runs: BackupRun[], ctx: Pick<SectionCtx, "scope" | "selected">, legacy: boolean): BackupRun[] {
  return runs.filter((r) => legacy || (ctx.scope === "instance" ? r.scope === "instance" : r.scope === "workspaces" && !!r.workspace_id && ctx.selected.has(r.workspace_id)))
}

/** Whether a run passes the panel's search and Filter. The panel counts with it too. */
export function matchesQuery(r: BackupRun, q: RunQuery | undefined, now: number = Date.now()): boolean {
  if (!q) return true
  const text = q.q.trim().toLowerCase()
  if (text && ![r.plan_name, r.workspace_name, r.note, r.scope === "instance" ? "whole instance" : null]
    .some((v) => v?.toLowerCase().includes(text))) return false
  if (q.kind && r.kind !== q.kind) return false
  if (q.proof && (r.proof_level ?? 0) < Number(q.proof)) return false
  if (q.plan && r.plan_name !== q.plan) return false
  if (q.period && now - Date.parse(r.started_at) > Number(q.period) * 86_400_000) return false
  return true
}

/** Whether a run belongs to a Runs facet. The side panel counts with it too. */
export function matches(r: BackupRun, f: RunFilter): boolean {
  switch (f) {
    case "failed": return r.status === "failed"
    case "incomplete": return r.status === "incomplete" || r.incomplete.length > 0
    case "manual": return r.trigger === "manual"
    case "pinned": return r.pinned
    default: return true
  }
}


const stackSub = (r: BackupRun) => `${r.scope === "instance" ? "Whole instance" : r.workspace_name ?? "—"} · ${runPlanLabel(r)} · ${formatSize(r.size_bytes)}`

/**
 * Backups › Runs: every run in the scope, what it went through, and how far
 * each backup is proven (Checksum, Contents checked, Test restore), never
 * folded into one "restorable". The server sends the newest 500; they are paged
 * here, and when 500 came back the table says so. A run opens in a side sheet.
 */
export function BackupsHistory({ ctx, resource }: { ctx: SectionCtx; resource?: Resource<BackupRun[]> & { source: "runs" | "legacy" } }) {
  if (resource) return <HistoryGate res={resource} ctx={ctx} />
  return <OwnHistory ctx={ctx} />
}

function OwnHistory({ ctx }: { ctx: SectionCtx }) {
  const res = useBackupRuns(ctx.scope, ctx.selected, ctx.workspaces)
  return <HistoryGate res={res} ctx={ctx} />
}

function HistoryGate({ res, ctx }: { res: Resource<BackupRun[]> & { source: "runs" | "legacy" }; ctx: SectionCtx }) {
  return (
    <Gate resource={res} what="Runs" skeleton="h-[280px]">
      {(runs) => <HistoryBody runs={runs} ctx={ctx} legacy={res.source === "legacy"} reload={res.reload} />}
    </Gate>
  )
}

export function HistoryBody({ runs, ctx, legacy, reload, now = new Date() }: { runs: BackupRun[]; ctx: SectionCtx; legacy?: boolean; reload?: () => void; now?: Date }) {
  // On the nested page the page owns the result chips; on its own the section
  // draws the same choice as a segmented control.
  const [localFilter, setLocalFilter] = React.useState<RunFilter>("all")
  const filter = ctx.inDrill ? ctx.runFilter ?? "all" : localFilter
  const [plan, setPlan] = React.useState<string>("")
  const [open, setOpen] = React.useState<string | null>(ctx.focusRun)
  const [checking, setChecking] = React.useState<BackupRun | null>(null)
  const [inside, setInside] = React.useState<BackupRun | null>(null)
  const [checked, setChecked] = React.useState<Record<string, { level: 1 | 2; ok: boolean; detail: string }>>({})

  const slice = ctx.runSlice
  const inScope = runsInScope(runs, ctx, !!legacy).filter((r) => matchesQuery(r, ctx.runQuery)
    && (!slice || (Date.parse(r.started_at) >= slice.fromMs && Date.parse(r.started_at) < slice.toMs)))
  // On the nested page rows can be ticked: the selection bar pins, unpins or
  // downloads several at once. A check and a restore stay one run at a time.
  const selectable = !!ctx.inDrill && !legacy
  const [picked, setPicked] = React.useState<Set<string>>(() => new Set())
  const plans = [...new Set(inScope.filter((r) => r.plan_name).map((r) => r.plan_name!))]
  const list = inScope.filter((r) => matches(r, filter) && (!plan || r.plan_name === plan))
  const paged = useShowMore(list, { step: 25, resetKey: `${filter}|${plan}|${JSON.stringify(ctx.runQuery)}|${slice?.fromMs}-${slice?.toMs}|${ctx.scope}|${[...ctx.selected].length}` })
  const current = runs.find((r) => r.id === open) ?? null
  const cap = legacy ? null : runsCapNote(runs.length)
  const pickedRuns = list.filter((r) => picked.has(r.id) && r.bundle_path)
  const pagedAllOn = paged.shown.length > 0 && paged.shown.every((r) => picked.has(r.id))
  const tick = (ids: string[], on: boolean) => setPicked((prev) => { const n = new Set(prev); for (const id of ids) { if (on) n.add(id); else n.delete(id) } return n })
  const pinAll = async (pin: boolean) => {
    let ok = 0
    for (const r of pickedRuns) if (r.pinned !== pin && await perform(ctx.demo, () => pinBundle(r.bundle_path!, pin), "", pin ? "Could not pin" : "Could not unpin")) ok++
    if (ok) toast.success(`${pin ? "Pinned" : "Unpinned"} ${ok} ${ok === 1 ? "backup" : "backups"}${pin ? " · rotation never deletes them" : ""}`)
    setPicked(new Set())
    reload?.()
  }
  const downloadAll = () => {
    for (const r of pickedRuns) {
      const href = downloadHref(r.bundle_path!, workspaceFor(ctx, r.workspace_id), r.scope)
      if (!href) continue
      const a = document.createElement("a")
      a.href = href; a.download = ""; a.rel = "noopener"
      document.body.appendChild(a); a.click(); a.remove()
    }
  }

  const columns: TableColumn<BackupRun>[] = [
    { key: "started", header: "Started", cell: (r) => formatWhen(r.started_at, now) },
    { key: "scope", header: "Scope", cell: (r) => (r.scope === "instance" ? <WsName name="Whole instance" instance /> : <WsName name={r.workspace_name ?? "—"} />) },
    { key: "plan", header: "Plan", cell: (r) => runPlanLabel(r), className: "min-w-[10rem]" },
    { key: "size", header: "Size", right: true, cell: (r) => <span className="font-mono">{formatSize(r.size_bytes)}</span> },
    { key: "result", header: "Result", cell: (r) => { const res = runResult(r); return <Chip tone={res.tone}>{res.text}</Chip> } },
    { key: "proof", header: "Checked to", cell: (r) => proofCell(r, checked[r.bundle_path ?? ""]?.level) },
    { key: "pin", header: <span className="sr-only">Pinned</span>, cell: (r) => (r.pinned ? <Pin className="h-3.5 w-3.5 text-muted-foreground" aria-label="Pinned" /> : null) },
  ]
  const lead = selectable ? {
    header: <input type="checkbox" aria-label="Select every run shown" checked={pagedAllOn} disabled={paged.shown.length === 0}
      onChange={(e) => tick(paged.shown.map((r) => r.id), e.target.checked)} className="size-3.5 accent-[var(--primary)]" />,
    cell: (r: BackupRun) => (
      <input type="checkbox" aria-label={`Select the run of ${formatWhen(r.started_at, now)}`} checked={picked.has(r.id)} disabled={!r.bundle_path}
        onChange={(e) => tick([r.id], e.target.checked)} className="size-3.5 accent-[var(--primary)] disabled:opacity-30 coarse:size-5" />
    ),
  } : undefined

  return (
    <>
      {legacy && (
        <p className="text-xs text-muted-foreground">The list of runs is not available on this server yet. These are the bundles it holds for the workspace you are in; a check proves the checksum only.</p>
      )}
      {!ctx.inDrill && (
        <div className="flex flex-wrap items-center gap-2">
          <SettingsSegmented label="Filter runs" options={FILTERS} value={localFilter} onChange={setLocalFilter} />
          {plans.length > 1 && (
            <select aria-label="Plan" value={plan} onChange={(e) => setPlan(e.target.value)} className={nativeSelect}>
              <option value="">All plans</option>
              {plans.map((p) => <option key={p} value={p}>{p}</option>)}
            </select>
          )}
        </div>
      )}
      <section aria-label="Runs" className="overflow-hidden rounded-card border border-border bg-card">
        <div className="flex items-center gap-3 border-b border-border px-4 py-3">
          <span className="icon-tile inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg" style={{ "--ic": "var(--primary)" } as React.CSSProperties} aria-hidden>
            <History className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1">
            <h3 className="text-sm font-semibold" data-slot="runs-count">{list.length === 1 ? "1 run" : `${list.length} runs`}</h3>
            <p className="mt-0.5 text-label text-muted-foreground">
              Newest first · open a run for its phases and proof
              {cap && <> · <span data-slot="runs-cap">this is {cap}; older ones are not listed</span></>}
            </p>
          </div>
        </div>
        <BackupsTable rows={paged.shown} rowKey={(r) => r.id} columns={columns} lead={lead} label="Runs"
          onOpen={(r) => setOpen(r.id)} isOpen={(r) => open === r.id} rowProps={(r) => ({ "data-run": r.id })}
          stack={(r) => ({ title: formatWhen(r.started_at, now), sub: stackSub(r), end: <Chip tone={runResult(r).tone}>{runResult(r).text}</Chip> })}
          empty={`No runs ${filter === "all" && !plan ? "in this scope yet" : "match this filter"}.`} />
        <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} noun="runs" paged={paged.paged} />
      </section>
      {selectable && pickedRuns.length > 0 && (
        <div role="toolbar" aria-label="Change the selected runs" data-slot="runs-bulk"
          className={cn(selectionBar, "fixed bottom-6 left-1/2 -translate-x-1/2 whitespace-nowrap",
            "max-md:inset-x-3 max-md:bottom-[calc(var(--mobile-tab-bar-h,0px)+0.75rem)] max-md:translate-x-0 max-md:justify-between max-md:gap-1 max-md:px-2")}>
          <span className="px-1"><span className="font-semibold tabular-nums">{pickedRuns.length}</span><span className="max-md:hidden"> selected</span></span>
          <Button type="button" size="sm" className={cn(selectionBarAction, "max-md:px-2")} onClick={() => void pinAll(true)}><Pin />Pin</Button>
          <Button type="button" size="sm" variant="outline" className={cn(selectionBarButton, "max-md:px-2")} onClick={() => void pinAll(false)}><PinOff />Unpin</Button>
          <Button type="button" size="sm" variant="outline" className={cn(selectionBarButton, "max-md:px-2")} onClick={downloadAll}><Download />Download</Button>
          <Button type="button" variant="ghost" size="icon-sm" className={cn(selectionBarButton, "w-8 px-0")} aria-label="Clear selection" onClick={() => setPicked(new Set())}>
            <X className="h-3.5 w-3.5" />
          </Button>
        </div>
      )}
      <Sheet open={!!current} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-2xl">
          {current && (
            <RunDrawer run={current} runs={runs} now={now} ctx={ctx} check={checked[current.bundle_path ?? ""]}
              onOpenRun={setOpen}
              onPin={async () => {
                if (!current.bundle_path) return
                const ok = await perform(ctx.demo, () => pinBundle(current.bundle_path!, !current.pinned), current.pinned ? "Unpinned" : "Pinned · rotation never deletes it", current.pinned ? "Could not unpin" : "Could not pin")
                if (ok) reload?.()
              }}
              onCheck={() => setChecking(current)} onInside={() => setInside(current)} />
          )}
        </SheetContent>
      </Sheet>
      <CheckDialog run={checking} legacy={!!legacy} onClose={() => setChecking(null)}
        onCheck={async (key) => {
          if (!checking?.bundle_path) return
          const out = await perform(ctx.demo, () => checkBundle(checking.bundle_path!, key, workspaceFor(ctx, checking.workspace_id)), "", "The check could not run")
          if (out) setChecked((c) => ({ ...c, [checking.bundle_path!]: out }))
          setChecking(null)
        }} />
      <ManifestDialog run={inside} demo={ctx.demo} onClose={() => setInside(null)} />
    </>
  )
}

/** The proof column: the three steps and the word for the highest reached. */
function proofCell(r: BackupRun, checkedLevel?: 1 | 2) {
  const level = Math.max(r.proof_level, checkedLevel ?? 0) as BackupRun["proof_level"]
  if (r.status === "failed" || r.status === "skipped" || (level === 0 && !r.bundle_path)) return <span className="text-muted-foreground">—</span>
  const partial = r.status === "incomplete" || r.incomplete.length > 0 || (level === 3 && !!r.drill_result && r.drill_result !== "ok")
  return <ProofSteps level={level} partial={partial} />
}

const PROOF_STEPS: { n: 1 | 2 | 3; label: string; about: string }[] = [
  { n: 1, label: "Checksum", about: "The file is what was written" },
  { n: 2, label: "Contents checked", about: "Every section was read and counted" },
  { n: 3, label: "Test restore", about: "It was restored into an isolated instance" },
]

function phaseValue(p: RunPhase): string {
  if (p.status === "skipped") return p.detail ? `skipped · ${p.detail}` : "skipped"
  if (p.status === "pending") return "—"
  if (p.status === "failed") return p.detail ?? "failed"
  return runDuration(p.started_at, p.ended_at) || p.detail || "done"
}

function RunDrawer({ run, runs, now, ctx, check, onPin, onCheck, onInside, onOpenRun }: {
  run: BackupRun; runs: BackupRun[]; now: Date; ctx: SectionCtx; check?: { level: 1 | 2; ok: boolean; detail: string }
  onPin: () => void; onCheck: () => void; onInside: () => void; onOpenRun: (id: string) => void
}) {
  const href = run.bundle_path ? downloadHref(run.bundle_path, workspaceFor(ctx, run.workspace_id), run.legacy ? undefined : run.scope) : null
  const proof = Math.max(run.proof_level, check?.ok ? check.level : 0)
  const missing = run.incomplete.reduce((a, i) => a + i.count, 0)
  const res = runResult(run)
  const took = runDuration(run.started_at, run.ended_at)
  const earlier = run.retry_of ? runs.find((r) => r.id === run.retry_of) : null
  const btn = "h-7 text-xs coarse:h-[44px]"
  const step = (n: 1 | 2 | 3): React.ReactNode => {
    if (n === 1) return proof >= 1 ? <StatusPill tone="success" label="Done" /> : check && !check.ok ? <StatusPill tone="danger" label="Does not match" /> : <StatusPill tone="muted" label="Not yet" />
    if (n === 2) return proof >= 2 ? <StatusPill tone="success" label="Done" /> : <StatusPill tone="muted" label="Not yet" />
    if (run.proof_level >= 3) return <StatusPill tone={run.drill_result === "ok" ? "success" : run.drill_result === "failed" ? "danger" : "warn"} label={run.drill_result === "ok" ? "Done" : run.drill_result ?? "partial"} />
    return <StatusPill tone="muted" label="Not yet" />
  }
  const stepNote = (n: 1 | 2 | 3, about: string): React.ReactNode => (
    <>
      {about}
      {n === 2 && missing > 0 && <span className="block text-warn">{run.incomplete.map((i) => i.detail).join(" · ")} absent</span>}
      {n === 2 && check && check.level === 2 && <span className="block">{check.detail}</span>}
      {n === 3 && run.drill_note && <span className="block">{run.drill_note}</span>}
    </>
  )
  return (
    <div data-slot="run-drawer" className="flex min-h-full flex-col">
      <SheetHeader>
        <SheetTitle className="text-sm">{formatWhen(run.started_at, now)}</SheetTitle>
        <SheetDescription>{run.scope === "instance" ? "Whole instance" : run.workspace_name ?? "—"} · {runPlanLabel(run)}</SheetDescription>
      </SheetHeader>
      <div className="grid flex-1 content-start gap-4 px-4 pb-4">
        <SettingsCard title="Result">
          <SettingsRow label="Result"><Chip tone={res.tone}>{res.text}</Chip></SettingsRow>
          {run.error && <SettingsRow label="Why"><span className="text-destructive">{run.error}</span></SettingsRow>}
          <SettingsRow label="Scope">{run.scope === "instance" ? "Whole instance" : run.workspace_name ?? "—"}</SettingsRow>
          <SettingsRow label="Size"><span className="font-mono">{formatSize(run.size_bytes)}</span></SettingsRow>
          <SettingsRow label="Format">
            {run.format_version ? `v${run.format_version}` : "—"}{run.restorable === "direct" ? " · restorable here directly" : run.restorable === "converter" ? " · through a converter; the original stays untouched" : run.restorable === "unsupported" ? " · not restorable by this server" : ""}
          </SettingsRow>
          {took && <SettingsRow label="Took">{took}</SettingsRow>}
          {run.hold_ms != null && <SettingsRow label="Held new work for" description="How long writes waited for a consistent copy"><span className="font-mono" data-slot="hold">{formatHold(run.hold_ms)}</span></SettingsRow>}
          <SettingsRow label="Started by">{TRIGGER_LABEL[run.trigger] ?? run.trigger}</SettingsRow>
          {run.note && <SettingsRow label="Note">{run.note}</SettingsRow>}
          {run.retry_of && (
            <SettingsRow label="Retry of">
              {earlier
                ? <Button type="button" variant="link" className="h-auto p-0 text-control" onClick={() => onOpenRun(earlier.id)}>{formatWhen(earlier.started_at, now)} · {runResult(earlier).text}</Button>
                : <span className="text-muted-foreground">an earlier run</span>}
            </SettingsRow>
          )}
        </SettingsCard>
        <SettingsCard title="Proof" description="How far this backup was checked">
          {PROOF_STEPS.map((p) => (
            <SettingsRow key={p.n} label={`${p.n} · ${p.label}`} description={stepNote(p.n, p.about)}>{step(p.n)}</SettingsRow>
          ))}
        </SettingsCard>
        <SettingsCard title="Phases">
          {run.phases.length === 0 && <p className="px-4 py-3 text-label text-muted-foreground">not recorded</p>}
          {run.phases.map((p) => <SettingsRow key={p.name} label={p.name}><span className="font-mono text-label">{phaseValue(p)}</span></SettingsRow>)}
        </SettingsCard>
        <SettingsCard title="Encryption">
          <SettingsRow label="Encrypted to">{run.recipients.length ? run.recipients.map((r) => `“${r}”`).join(", ") : "AGE (always encrypted)"}</SettingsRow>
        </SettingsCard>
      </div>
      <SheetFooter className="flex-row flex-wrap gap-1.5 border-t border-border">
        {!run.legacy && <Button type="button" size="sm" variant="outline" className={btn} onClick={onPin}>{run.pinned ? "Unpin" : "Pin"}</Button>}
        <Button type="button" size="sm" variant="outline" className={btn} onClick={onCheck} disabled={!run.bundle_path}>Check contents…</Button>
        {!run.legacy && <Button type="button" size="sm" variant="outline" className={btn} onClick={onInside} disabled={!run.bundle_path}>What is inside</Button>}
        {href
          ? <Button size="sm" variant="outline" className={btn} asChild><a href={href} download>Download</a></Button>
          : <Button type="button" size="sm" variant="outline" className={btn} disabled>Download</Button>}
        <Button type="button" size="sm" className={cn(btn, "sm:ml-auto")} onClick={() => ctx.go("recovery", { path: run.bundle_path ?? undefined })} disabled={!run.bundle_path}>Restore…</Button>
      </SheetFooter>
    </div>
  )
}

function CheckDialog({ run, legacy, onClose, onCheck }: {
  run: BackupRun | null; legacy: boolean; onClose: () => void; onCheck: (key: { identity?: string; passphrase?: string }) => Promise<void>
}) {
  const [identity, setIdentity] = React.useState("")
  const [passphrase, setPassphrase] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  React.useEffect(() => { if (!run) { setIdentity(""); setPassphrase(""); setBusy(false) } }, [run])
  const close = () => { if (!busy) onClose() }
  const canCheck = !busy && (legacy || !!identity.trim() || !!passphrase)
  const check = async () => {
    if (!canCheck) return
    setBusy(true)
    await onCheck(legacy ? {} : { identity: identity.trim() || undefined, passphrase: passphrase || undefined })
  }
  return (
    <CreateSurface open={!!run} onOpenChange={(o) => { if (!o) close() }} size="sm" onSubmit={() => void check()}>
        <CreateSurfaceHeader
          title="Check contents"
          description={<>
            Decrypts the backup and reads every section, comparing row counts and hashes with what it recorded. Nothing is restored.
            The key is used for this check only and never stored.
          </>}
          onClose={close}
        />
        <CreateSurfaceBody>
        {legacy ? (
          <p className="text-xs text-muted-foreground">This server can only verify the checksum for now, which needs no key.</p>
        ) : (
          <div className="flex flex-col gap-2 text-control">
            <label className="flex flex-col gap-1">
              <span className="text-muted-foreground">AGE identity (private key)</span>
              <textarea value={identity} onChange={(e) => setIdentity(e.target.value)} rows={2} spellCheck={false} placeholder="AGE-SECRET-KEY-1…"
                className="rounded-md border border-control-border bg-surface-subtle px-2 py-1.5 font-mono text-label" />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-muted-foreground">or passphrase (older bundles)</span>
              <input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoComplete="off"
                className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 coarse:h-[44px]" />
            </label>
          </div>
        )}
        </CreateSurfaceBody>
        <CreateSurfaceFooter
          onCancel={close}
          primaryLabel={busy ? "Checking…" : "Check"}
          onPrimary={() => void check()}
          primaryDisabled={!canCheck}
          busy={busy}
        />
    </CreateSurface>
  )
}
