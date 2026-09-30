"use client"

import * as React from "react"
import { Pin } from "lucide-react"

import { cn } from "@/lib/utils"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Chip, Gate, SmallButton, TD, TH, WsName } from "./backups-kit"
import { formatPhases, formatSize, formatWhen, proofLabel, runPlanLabel, runResult, type BackupRun } from "./backups-model"
import { checkBundle, downloadHref, pinBundle, useBackupRuns } from "./use-backup-runs"
import { perform } from "./use-backups-data"
import type { SectionCtx } from "./backups-console"

type Filter = { kind: "all" } | { kind: "plan"; name: string } | { kind: "manual" } | { kind: "pinned" }

/**
 * Backups › Backup history: every run in the scope, what it went through, and
 * how far each backup is proven — checksum, contents checked, test restore —
 * never folded into one "restorable".
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
  const [filter, setFilter] = React.useState<Filter>({ kind: "all" })
  const [open, setOpen] = React.useState<string | null>(ctx.focusRun)
  const [checking, setChecking] = React.useState<BackupRun | null>(null)
  const [checked, setChecked] = React.useState<Record<string, { level: 1 | 2; ok: boolean; detail: string }>>({})

  const inScope = runs.filter((r) => legacy || (ctx.scope === "instance" ? r.scope === "instance" : r.scope === "workspaces" && !!r.workspace_id && ctx.selected.has(r.workspace_id)))
  const plans = [...new Set(inScope.filter((r) => r.plan_name).map((r) => r.plan_name!))]
  const list = inScope.filter((r) =>
    filter.kind === "all" ? true
    : filter.kind === "plan" ? r.plan_name === filter.name
    : filter.kind === "manual" ? r.trigger === "manual"
    : r.pinned)
  const current = inScope.find((r) => r.id === open) ?? null

  const chip = (f: Filter, label: string) => {
    const on = JSON.stringify(f) === JSON.stringify(filter)
    return (
      <button key={label} type="button" aria-pressed={on} onClick={() => setFilter(f)}
        className={cn("h-6 rounded-full border px-2.5 text-[12px] coarse:h-[2.75rem]",
          on ? "border-accent bg-accent text-foreground" : "border-control-border text-muted-foreground hover:text-foreground")}>
        {label}
      </button>
    )
  }

  return (
    <>
      {legacy && (
        <p className="text-[12.5px] text-muted-foreground">Run history is not available on this server yet. These are the bundles it holds for the workspace you are in; a check proves the checksum only.</p>
      )}
      <div role="group" aria-label="Filter runs" className="flex flex-wrap items-center gap-1.5">
        {chip({ kind: "all" }, "All plans")}
        {plans.map((p) => chip({ kind: "plan", name: p }, p))}
        {chip({ kind: "manual" }, "Manual")}
        {chip({ kind: "pinned" }, "Pinned")}
      </div>
      <div className="overflow-hidden rounded-card border border-border bg-card">
        <div className="overflow-x-auto">
          <table className="w-full tabular-nums">
            <thead><tr>{["Started", "Scope", "Plan", "Size", "Result", "Checked to"].map((h) => <th key={h} className={TH}>{h}</th>)}<th className={TH}><span className="sr-only">Pinned</span></th></tr></thead>
            <tbody>
              {list.length === 0 && <tr><td colSpan={7} className="px-4 py-4 text-center text-[12.5px] text-muted-foreground">No runs {filter.kind === "all" ? "in this scope yet" : "match this filter"}.</td></tr>}
              {list.map((r) => {
                const res = runResult(r)
                const proof = checked[r.bundle_path ?? ""]?.level ?? r.proof_level
                return (
                  <tr key={r.id} tabIndex={0} aria-selected={open === r.id} data-run={r.id}
                    onClick={() => setOpen(open === r.id ? null : r.id)}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setOpen(open === r.id ? null : r.id) } }}
                    className="cursor-pointer hover:[&>td]:bg-muted aria-selected:[&>td]:bg-muted [&:last-child>td]:border-b-0">
                    <td className={TD}>{formatWhen(r.started_at, now)}</td>
                    <td className={TD}>{r.scope === "instance" ? <WsName name="Whole instance" instance /> : <WsName name={r.workspace_name ?? "—"} />}</td>
                    <td className={cn(TD, "min-w-[10rem] whitespace-normal")}>{runPlanLabel(r)}</td>
                    <td className={TD}>{formatSize(r.size_bytes)}</td>
                    <td className={TD}><Chip tone={res.tone}>{res.text}</Chip></td>
                    <td className={TD}>{proofLabel(Math.max(proof, r.proof_level) as BackupRun["proof_level"], r.drill_result)}</td>
                    <td className={TD}>{r.pinned && <Pin className="h-3.5 w-3.5 text-muted-foreground" aria-label="Pinned" />}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </div>
      {current && (
        <RunDrawer run={current} now={now} ctx={ctx} check={checked[current.bundle_path ?? ""]}
          onPin={async () => {
            if (!current.bundle_path) return
            const ok = await perform(ctx.demo, () => pinBundle(current.bundle_path!, !current.pinned), current.pinned ? "Unpinned" : "Pinned · rotation never deletes it", current.pinned ? "Could not unpin" : "Could not pin")
            if (ok) reload?.()
          }}
          onCheck={() => setChecking(current)} />
      )}
      <CheckDialog run={checking} legacy={!!legacy} onClose={() => setChecking(null)}
        onCheck={async (key) => {
          if (!checking?.bundle_path) return
          const out = await perform(ctx.demo, () => checkBundle(checking.bundle_path!, key, ctx.currentWorkspaceId), "", "The check could not run")
          if (out) setChecked((c) => ({ ...c, [checking.bundle_path!]: out }))
          setChecking(null)
        }} />
    </>
  )
}

function RunDrawer({ run, now, ctx, check, onPin, onCheck }: {
  run: BackupRun; now: Date; ctx: SectionCtx; check?: { level: 1 | 2; ok: boolean; detail: string }
  onPin: () => void; onCheck: () => void
}) {
  const href = run.bundle_path ? downloadHref(run.bundle_path, ctx.currentWorkspaceId) : null
  const proof = Math.max(run.proof_level, check?.ok ? check.level : 0)
  const missing = run.incomplete.reduce((a, i) => a + i.count, 0)
  return (
    <section data-slot="run-drawer" aria-label="Run details" className="flex flex-col gap-2.5 rounded-card border border-border bg-card px-3.5 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-[13px] font-semibold">{runPlanLabel(run)} · {formatWhen(run.started_at, now)}</h3>
        <span className="flex-1" />
        {!run.legacy && <SmallButton onClick={onPin}>{run.pinned ? "Unpin" : "Pin"}</SmallButton>}
        <SmallButton onClick={onCheck} disabled={!run.bundle_path}>Check contents…</SmallButton>
        {href ? <SmallButton asChild><a href={href} download>Download</a></SmallButton> : <SmallButton disabled>Download</SmallButton>}
        <SmallButton primary onClick={() => ctx.go("recovery", { path: run.bundle_path ?? undefined })} disabled={!run.bundle_path}>Restore…</SmallButton>
      </div>
      <dl className="grid grid-cols-1 gap-x-3 gap-y-1.5 text-[13px] md:grid-cols-[170px_minmax(0,1fr)]">
        <dt className="text-muted-foreground">Phases</dt>
        <dd>{run.phases.length ? formatPhases(run.phases) : "not recorded"}{run.error && <span className="text-destructive"> · {run.error}</span>}</dd>
        <dt className="text-muted-foreground">Encryption</dt>
        <dd>{run.recipients.length ? `AGE · ${run.recipients.length === 1 ? "recipient" : "recipients"} ${run.recipients.map((r) => `“${r}”`).join(", ")} (always encrypted)` : "AGE (always encrypted)"}</dd>
        <dt className="text-muted-foreground">1 · Checksum</dt>
        <dd>{proof >= 1 ? <Chip tone="ok">✓ matches</Chip> : check && !check.ok ? <Chip tone="bad">does not match</Chip> : "—"}</dd>
        <dt className="text-muted-foreground">2 · Contents checked</dt>
        <dd className="flex flex-wrap items-center gap-2">
          {proof >= 2 ? <Chip tone="ok">✓ every section read</Chip> : "not yet"}
          {missing > 0 && <span className="text-warn">{run.incomplete.map((i) => i.detail).join(" · ")} absent</span>}
          {check && <span className="text-muted-foreground">{check.detail}</span>}
        </dd>
        <dt className="text-muted-foreground">3 · Test restore</dt>
        <dd>{run.proof_level >= 3 ? (run.drill_result === "ok" ? <Chip tone="ok">passed</Chip> : <><Chip tone={run.drill_result === "failed" ? "bad" : "warn"}>{run.drill_result ?? "partial"}</Chip> {run.drill_note}</>) : "never"}</dd>
        <dt className="text-muted-foreground">Format</dt>
        <dd>{run.format_version ? `v${run.format_version}` : "—"}{run.restorable === "direct" ? " · restorable here directly" : run.restorable === "converter" ? " · restorable through a converter; the original stays untouched" : run.restorable === "unsupported" ? " · not restorable by this server" : ""}</dd>
      </dl>
    </section>
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
          <p className="text-[12.5px] text-muted-foreground">This server can only verify the checksum for now, which needs no key.</p>
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
          <SmallButton onClick={onClose} disabled={busy}>Cancel</SmallButton>
          <SmallButton primary disabled={busy || (!legacy && !identity.trim() && !passphrase)}
            onClick={async () => { setBusy(true); await onCheck(legacy ? {} : { identity: identity.trim() || undefined, passphrase: passphrase || undefined }) }}>
            {busy ? "Checking…" : "Check"}
          </SmallButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
