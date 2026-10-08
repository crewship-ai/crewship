"use client"

import * as React from "react"
import { History, Pin } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { SettingsSegmented, SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { Chip, Gate, WsName } from "./backups-kit"
import { formatPhases, formatSize, formatWhen, proofLabel, runPlanLabel, runResult, type BackupRun } from "./backups-model"
import { checkBundle, downloadHref, pinBundle, useBackupRuns, workspaceFor } from "./use-backup-runs"
import { perform } from "./use-backups-data"
import type { RunFilter, SectionCtx } from "./backups-console"

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

const TH = "px-3 py-2 text-left font-medium"
const TD = "px-3 py-2.5 align-top"

/**
 * Backups › Backup history: every run in the scope, what it went through, and
 * how far each backup is proven — checksum, contents checked, test restore —
 * never folded into one "restorable". Built like Security › Activity: a
 * summary line, one table, and a run opens in a side sheet.
 */
export function BackupsHistory({ ctx }: { ctx: SectionCtx }) {
  const res = useBackupRuns(ctx.scope, ctx.selected, ctx.workspaces)
  return (
    <Gate resource={res} what="Backup history" skeleton="h-[280px]">
      {(runs) => <HistoryBody runs={runs} ctx={ctx} legacy={res.source === "legacy"} reload={res.reload} />}
    </Gate>
  )
}

export function HistoryBody({ runs, ctx, legacy, reload, now = new Date() }: { runs: BackupRun[]; ctx: SectionCtx; legacy?: boolean; reload?: () => void; now?: Date }) {
  // On the nested page the side panel owns the facet; on its own the section
  // draws the same choice as a segmented control.
  const [localFilter, setLocalFilter] = React.useState<RunFilter>("all")
  const filter = ctx.inDrill ? ctx.runFilter ?? "all" : localFilter
  const [plan, setPlan] = React.useState<string>("")
  const [open, setOpen] = React.useState<string | null>(ctx.focusRun)
  const [checking, setChecking] = React.useState<BackupRun | null>(null)
  const [checked, setChecked] = React.useState<Record<string, { level: 1 | 2; ok: boolean; detail: string }>>({})

  const inScope = runsInScope(runs, ctx, !!legacy)
  const plans = [...new Set(inScope.filter((r) => r.plan_name).map((r) => r.plan_name!))]
  const list = inScope.filter((r) => matches(r, filter) && (!plan || r.plan_name === plan))
  const current = inScope.find((r) => r.id === open) ?? null
  const count = (f: RunFilter) => inScope.filter((r) => matches(r, f)).length
  const nFailed = count("failed"), nIncomplete = count("incomplete"), nPinned = count("pinned")
  const filterLabel = FILTERS.find((f) => f.value === filter)?.label

  return (
    <>
      {legacy && (
        <p className="text-xs text-muted-foreground">Run history is not available on this server yet. These are the bundles it holds for the workspace you are in; a check proves the checksum only.</p>
      )}
      <SettingsSummary>
        <SummaryItem n={inScope.length}>run{inScope.length === 1 ? "" : "s"}</SummaryItem>
        <SummaryItem n={nFailed} tone={nFailed ? "danger" : undefined}>failed</SummaryItem>
        <SummaryItem n={nIncomplete} tone={nIncomplete ? "warn" : undefined}>incomplete</SummaryItem>
        <SummaryItem n={nPinned}>pinned</SummaryItem>
        {ctx.inDrill && filter !== "all" && (
          <span data-slot="run-filter" className="inline-flex h-6 items-center rounded-full bg-primary/10 px-2.5 text-[11.5px] text-primary-hover">
            Showing {filterLabel}
          </span>
        )}
      </SettingsSummary>
      {(!ctx.inDrill || plans.length > 1) && (
        <div className="flex flex-wrap items-center gap-2">
          {!ctx.inDrill && <SettingsSegmented label="Filter runs" options={FILTERS} value={localFilter} onChange={setLocalFilter} />}
          {plans.length > 1 && (
            <select aria-label="Plan" value={plan} onChange={(e) => setPlan(e.target.value)}
              className="h-8 rounded-md border border-control-border bg-surface-subtle px-2.5 text-xs coarse:h-[2.75rem]">
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
            <h3 className="text-sm font-semibold">{filter === "all" ? "Every run" : `${filterLabel} runs`}</h3>
            <p className="mt-0.5 text-[12px] text-muted-foreground">Newest first · open a run for its phases and proof</p>
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-[12.5px] tabular-nums">
            <thead className="border-b border-border">
              <tr className="text-[11px] text-muted-foreground">
                {["Started", "Scope", "Plan", "Size", "Result", "Checked to"].map((h) => <th key={h} className={cn(TH, h === "Size" && "text-right")}>{h}</th>)}
                <th className={TH}><span className="sr-only">Pinned</span></th>
              </tr>
            </thead>
            <tbody>
              {list.length === 0 && <tr><td colSpan={7} className="px-3 py-8 text-center text-[12px] text-muted-foreground">No runs {filter === "all" && !plan ? "in this scope yet" : "match this filter"}.</td></tr>}
              {list.map((r) => {
                const res = runResult(r)
                const proof = checked[r.bundle_path ?? ""]?.level ?? r.proof_level
                return (
                  <tr key={r.id} tabIndex={0} aria-selected={open === r.id} data-run={r.id}
                    onClick={() => setOpen(r.id)}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setOpen(r.id) } }}
                    className="cursor-pointer border-b border-border last:border-b-0 hover:bg-accent focus-visible:bg-accent focus-visible:outline-none aria-selected:bg-accent">
                    <td className={cn(TD, "whitespace-nowrap")}>{formatWhen(r.started_at, now)}</td>
                    <td className={cn(TD, "whitespace-nowrap")}>{r.scope === "instance" ? <WsName name="Whole instance" instance /> : <WsName name={r.workspace_name ?? "—"} />}</td>
                    <td className={cn(TD, "min-w-[10rem]")}>{runPlanLabel(r)}</td>
                    <td className={cn(TD, "whitespace-nowrap text-right font-mono")}>{formatSize(r.size_bytes)}</td>
                    <td className={TD}><Chip tone={res.tone}>{res.text}</Chip></td>
                    <td className={cn(TD, "text-muted-foreground")}>{proofLabel(Math.max(proof, r.proof_level) as BackupRun["proof_level"], r.drill_result)}</td>
                    <td className={TD}>{r.pinned && <Pin className="h-3.5 w-3.5 text-muted-foreground" aria-label="Pinned" />}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </section>
      <Sheet open={!!current} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-2xl">
          {current && (
            <RunDrawer run={current} now={now} ctx={ctx} check={checked[current.bundle_path ?? ""]}
              onPin={async () => {
                if (!current.bundle_path) return
                const ok = await perform(ctx.demo, () => pinBundle(current.bundle_path!, !current.pinned), current.pinned ? "Unpinned" : "Pinned · rotation never deletes it", current.pinned ? "Could not unpin" : "Could not pin")
                if (ok) reload?.()
              }}
              onCheck={() => setChecking(current)} />
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
    </>
  )
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-[11px] text-muted-foreground">{label}</dt>
      <dd className="mt-0.5">{children}</dd>
    </div>
  )
}

/** One rung of the proof ladder: its number, what it proves, and where this run stands. */
function Rung({ n, label, children }: { n: 1 | 2 | 3; label: string; children: React.ReactNode }) {
  return (
    <li className="flex items-start gap-3 border-b border-border py-2.5 last:border-b-0">
      <span aria-hidden className="grid h-5 w-5 shrink-0 place-items-center rounded-full bg-muted font-mono text-[11px] text-muted-foreground">{n}</span>
      <div className="min-w-0 flex-1">
        <div>{label}</div>
        <div className="mt-1 flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">{children}</div>
      </div>
    </li>
  )
}

function RunDrawer({ run, now, ctx, check, onPin, onCheck }: {
  run: BackupRun; now: Date; ctx: SectionCtx; check?: { level: 1 | 2; ok: boolean; detail: string }
  onPin: () => void; onCheck: () => void
}) {
  const href = run.bundle_path ? downloadHref(run.bundle_path, workspaceFor(ctx, run.workspace_id), run.legacy ? undefined : run.scope) : null
  const proof = Math.max(run.proof_level, check?.ok ? check.level : 0)
  const missing = run.incomplete.reduce((a, i) => a + i.count, 0)
  const res = runResult(run)
  const btn = "h-7 text-xs"
  return (
    <div data-slot="run-drawer" className="flex min-h-full flex-col">
      <SheetHeader>
        <SheetTitle className="text-sm">{runPlanLabel(run)} · {formatWhen(run.started_at, now)}</SheetTitle>
        <SheetDescription className="sr-only">Phases, encryption and how far this backup is proven</SheetDescription>
      </SheetHeader>
      <div className="grid flex-1 gap-5 px-4 pb-4 text-[12.5px]">
        <dl className="grid grid-cols-2 gap-3">
          <Fact label="Result"><Chip tone={res.tone}>{res.text}</Chip></Fact>
          <Fact label="Scope">{run.scope === "instance" ? "Whole instance" : run.workspace_name ?? "—"}</Fact>
          <Fact label="Size"><span className="font-mono">{formatSize(run.size_bytes)}</span></Fact>
          <Fact label="Format">{run.format_version ? `v${run.format_version}` : "—"}{run.restorable === "direct" ? " · restorable here directly" : run.restorable === "converter" ? " · restorable through a converter; the original stays untouched" : run.restorable === "unsupported" ? " · not restorable by this server" : ""}</Fact>
        </dl>
        <section aria-label="Proof">
          <h4 className="eyebrow mb-1">Proof</h4>
          <ol>
            <Rung n={1} label="Checksum">
              {proof >= 1 ? <Chip tone="ok">✓ matches</Chip> : check && !check.ok ? <Chip tone="bad">does not match</Chip> : "not checked"}
            </Rung>
            <Rung n={2} label="Contents checked">
              {proof >= 2 ? <Chip tone="ok">✓ every section read</Chip> : "not yet"}
              {missing > 0 && <span className="text-warn">{run.incomplete.map((i) => i.detail).join(" · ")} absent</span>}
              {check && <span>{check.detail}</span>}
            </Rung>
            <Rung n={3} label="Test restore">
              {run.proof_level >= 3
                ? (run.drill_result === "ok" ? <Chip tone="ok">passed</Chip> : <><Chip tone={run.drill_result === "failed" ? "bad" : "warn"}>{run.drill_result ?? "partial"}</Chip>{run.drill_note && <span>{run.drill_note}</span>}</>)
                : "never"}
            </Rung>
          </ol>
        </section>
        <section aria-label="Phases">
          <h4 className="eyebrow mb-1">Phases</h4>
          <p>{run.phases.length ? formatPhases(run.phases) : "not recorded"}{run.error && <span className="text-destructive"> · {run.error}</span>}</p>
        </section>
        <section aria-label="Encryption">
          <h4 className="eyebrow mb-1">Encryption</h4>
          <p>{run.recipients.length ? `AGE · ${run.recipients.length === 1 ? "recipient" : "recipients"} ${run.recipients.map((r) => `“${r}”`).join(", ")} (always encrypted)` : "AGE (always encrypted)"}</p>
        </section>
      </div>
      <SheetFooter className="flex-row flex-wrap gap-1.5 border-t border-border">
        {!run.legacy && <Button type="button" size="sm" variant="outline" className={btn} onClick={onPin}>{run.pinned ? "Unpin" : "Pin"}</Button>}
        <Button type="button" size="sm" variant="outline" className={btn} onClick={onCheck} disabled={!run.bundle_path}>Check contents…</Button>
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
  return (
    <Dialog open={!!run} onOpenChange={(o) => { if (!o && !busy) onClose() }}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Check contents</DialogTitle>
          <DialogDescription>
            Decrypts the backup and reads every section, comparing row counts and hashes with what it recorded. Nothing is restored.
            The key is used for this check only and never stored.
          </DialogDescription>
        </DialogHeader>
        {legacy ? (
          <p className="text-xs text-muted-foreground">This server can only verify the checksum for now, which needs no key.</p>
        ) : (
          <div className="flex flex-col gap-2 text-[13px]">
            <label className="flex flex-col gap-1">
              <span className="text-muted-foreground">AGE identity (private key)</span>
              <textarea value={identity} onChange={(e) => setIdentity(e.target.value)} rows={2} spellCheck={false} placeholder="AGE-SECRET-KEY-1…"
                className="rounded-md border border-control-border bg-surface-subtle px-2 py-1.5 font-mono text-[12px]" />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-muted-foreground">or passphrase (older bundles)</span>
              <input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} autoComplete="off"
                className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 coarse:h-[2.75rem]" />
            </label>
          </div>
        )}
        <DialogFooter>
          <Button type="button" size="sm" variant="ghost" className="h-7 text-xs" onClick={onClose} disabled={busy}>Cancel</Button>
          <Button type="button" size="sm" className="h-7 text-xs" disabled={busy || (!legacy && !identity.trim() && !passphrase)}
            onClick={async () => { setBusy(true); await onCheck(legacy ? {} : { identity: identity.trim() || undefined, passphrase: passphrase || undefined }) }}>
            {busy ? "Checking…" : "Check"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
