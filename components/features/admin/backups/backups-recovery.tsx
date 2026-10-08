"use client"

import * as React from "react"
import Link from "next/link"
import { toast } from "sonner"
import {
  Archive, Cloud, FlaskConical, History, KeyRound, ListChecks, ListTodo, MapPin, PauseCircle, ShieldCheck, Terminal, TriangleAlert, CircleCheck,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsEmpty, SettingsRow, SettingsSegmented, settingsControl, settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { StatusPill } from "@/components/ui/status-pill"

type StatusTone = NonNullable<React.ComponentProps<typeof StatusPill>["tone"]>
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Gate, Unavailable } from "./backups-kit"
import {
  formatSize, formatSourceDate, formatWhen, proofLabel, runPlanLabel, shortDate,
  type BackupRun, type RestoreChecks, type RestoreRecord, type RestoreTarget,
} from "./backups-model"
import { checksFixture, dryRunFixture, restorePhasesFixture } from "./__fixtures__/backups"
import { useBackupRuns, workspaceFor } from "./use-backup-runs"
import {
  bringBackCrewFiles, fetchAndWait, restore, restoreChecks, resumeHold, useHolds, useOffsiteCopies, useRestores,
  type RestoreOutcome, type WizardTarget,
} from "./use-backup-recovery"
import { saveBackupSettings, useBackupSettings, useDestinations, useVaultKeys } from "./use-backup-settings"
import { perform } from "./use-backups-data"
import type { SectionCtx } from "./backups-console"

import { BackupsUpload, type UploadReceipt } from "./backups-upload"

type Sub = "new" | "history" | "drills"
/** Steps of an online restore; a "What" step (part of an archive) comes with the server's restore plan. */
const STEPS = ["Backup", "Where", "Keys", "Checks", "Restore"] as const
/** An instance archive restores offline, where the key is: the command after the checks, then what stays held. */
const CLI_STEPS = ["Backup", "Where", "Keys", "Checks", "Command line", "Resume"] as const

/** The file an admin keeps the private backup key in; the server never has it. */
export const KEY_FILE_PLACEHOLDER = "<key-file>"

/** Quote a path for a POSIX shell when it needs it. */
export function shellQuote(s: string): string {
  return /^[A-Za-z0-9_@%+=:,./-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`
}

/**
 * The offline command for an instance target: `crewship recover` onto an empty
 * server, or `crewship backup drill … --post` for an isolated test restore
 * that records its result on this server.
 */
export function offlineCommand(target: RestoreTarget, bundle: string | null): string {
  const b = bundle ? shellQuote(bundle) : "<bundle>"
  return target === "isolated"
    ? `crewship backup drill --bundle ${b} --identity ${KEY_FILE_PLACEHOLDER} --post`
    : `crewship recover --bundle ${b} --identity ${KEY_FILE_PLACEHOLDER} --data-dir /var/lib/crewship`
}

/**
 * What kind of archive a backup is. It decides where it may go: the server
 * restores an instance archive only offline, refuses --replace from a partial
 * (custom) archive and anything but a workspace archive, and --as-crew on
 * anything but a crew archive (internal/backup/runner_restore.go). "unknown"
 * is a bundle whose kind this page cannot read (a path handed over without its
 * run); it keeps the full workspace choice and the checks decide.
 */
export type ArchiveKind = "instance" | "workspace" | "crew" | "custom" | "unknown"

export function archiveKind(r: Pick<BackupRun, "scope" | "kind" | "legacy" | "workspace_name">): ArchiveKind {
  if (r.scope === "instance") return "instance"
  // The legacy bundle list names a crew bundle "<workspace> · crew" (legacyRun).
  if (r.legacy && /· crew$/.test(r.workspace_name ?? "")) return "crew"
  if (r.kind === "custom") return "custom"
  return "workspace"
}

interface TargetOption { key: WizardTarget; label: string; about: React.ReactNode; named?: "workspace" | "crew" }

const TARGETS: Record<ArchiveKind, TargetOption[]> = {
  instance: [
    { key: "empty_server", label: "Empty server", about: <>The whole instance, from the command line on the new server: <code className="font-mono text-xs">crewship recover --bundle … --identity ops-2026.key</code></> },
    { key: "isolated", label: "Isolated instance", about: "The same restore into a throwaway instance, to prove the backup." },
  ],
  workspace: [
    { key: "new_workspace", label: "Into a new workspace", about: "A copy beside the original; nothing that exists is touched.", named: "workspace" },
    { key: "replace", label: "Replace a workspace", about: "The workspace goes back to the backup; newer work in it is lost." },
  ],
  custom: [
    { key: "new_workspace", label: "Into a new workspace", about: "Only what the archive holds, beside what exists.", named: "workspace" },
  ],
  crew: [
    { key: "crew", label: "Crew under a new name", about: "Lands as a new workspace of that name, holding just this crew; nothing that exists is touched.", named: "crew" },
    { key: "crew_in_place", label: "The crew where it was", about: "Back under its own name; conflicts are checked first." },
  ],
  unknown: [
    { key: "new_workspace", label: "Into a new workspace", about: "A copy beside the original; nothing that exists is touched.", named: "workspace" },
    { key: "replace", label: "Replace a workspace", about: "The workspace goes back to the backup; newer work in it is lost." },
    { key: "crew", label: "One crew", about: "Into an existing workspace, under a new name.", named: "crew" },
  ],
}

const RESULT_TONE: Record<string, StatusTone> = { ok: "success", partial: "warn", failed: "danger" }

/**
 * Backups › Recovery: a guided restore — pick a backup, where it goes, the
 * keys; checks and a dry run before anything changes, then the server's own
 * verdict and what is left to finish. Plus every restore so far, and drills.
 * On the nested page the side panel picks the tab.
 */
export function BackupsRecovery({ ctx }: { ctx: SectionCtx }) {
  const [own, setOwn] = React.useState<Sub>("new")
  const sub: Sub = ctx.inDrill ? ctx.recoveryView ?? "new" : own
  return (
    <>
      {!ctx.inDrill && (
        <SettingsSegmented<Sub> label="Recovery" value={sub} onChange={setOwn}
          options={[{ value: "new", label: "New restore" }, { value: "history", label: "History" }, { value: "drills", label: "Drills" }]} />
      )}
      {sub === "new" && <RestoreWizard ctx={ctx} />}
      {sub === "history" && <RestoreHistory />}
      {sub === "drills" && <Drills ctx={ctx} />}
    </>
  )
}

interface Source { path: string; kind: ArchiveKind }

export function RestoreWizard({ ctx, now = new Date() }: { ctx: SectionCtx; now?: Date }) {
  const runs = useBackupRuns(ctx.scope, ctx.selected, ctx.workspaces)
  const vault = useVaultKeys()
  const instance = ctx.scope === "instance"
  const pool = (runs.data ?? []).filter((r) => r.bundle_path && (runs.source === "legacy" || (instance ? r.scope === "instance" : r.scope === "workspaces")))
  const kindOfPath = (p: string): ArchiveKind => {
    const r = pool.find((x) => x.bundle_path === p)
    return r ? archiveKind(r) : instance ? "instance" : "unknown"
  }
  const [step, setStep] = React.useState(ctx.focusPath ? 2 : 1)
  const [source, setSource] = React.useState<Source | null>(ctx.focusPath ? { path: ctx.focusPath, kind: kindOfPath(ctx.focusPath) } : null)
  // A focused path whose run arrives after the first render takes its kind then.
  const kind = source ? (source.kind === "unknown" || (source.kind === "instance" && ctx.focusPath === source.path) ? kindOfPath(source.path) : source.kind) : "unknown"
  const options = TARGETS[kind]
  const [chosen, setChosen] = React.useState<WizardTarget | null>(null)
  const target: WizardTarget = chosen && options.some((o) => o.key === chosen) ? chosen : options[0].key
  const option = options.find((o) => o.key === target)!
  const [asName, setAsName] = React.useState("")
  const [identity, setIdentity] = React.useState("")
  const [passphrase, setPassphrase] = React.useState("")
  const [checks, setChecks] = React.useState<RestoreChecks | "unavailable" | null>(null)
  const [report, setReport] = React.useState<RestoreOutcome | null>(null)
  const [outcome, setOutcome] = React.useState<RestoreOutcome | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [confirm, setConfirm] = React.useState(false)
  const [offsite, setOffsite] = React.useState(false)
  const [uploaded, setUploaded] = React.useState<UploadReceipt | null>(null)
  const [held, setHeld] = React.useState(false)
  const path = source?.path ?? null
  const cliOnly = target === "empty_server" || target === "isolated"
  const key = { identity: identity.trim() || undefined, passphrase: passphrase || undefined }
  const req = {
    path: path ?? "", target, ...key,
    as_workspace: option.named === "workspace" ? asName : undefined,
    as_crew: option.named === "crew" ? asName : undefined,
  }
  const picked = pool.find((r) => r.bundle_path === path) ?? null
  // The workspace a workspace/crew restore acts on, named explicitly.
  const restoreWs = workspaceFor(ctx, picked?.workspace_id)
  const steps = cliOnly ? CLI_STEPS : STEPS
  const current = cliOnly && held ? 6 : step

  const pick = (s: Source) => { setSource(s); setChosen(null); setStep(2) }

  const runChecks = async () => {
    setBusy(true)
    if (ctx.demo) setChecks(checksFixture(ctx.scope))
    else {
      const r = await restoreChecks(req)
      if (!r.ok && !r.unavailable) {
        setBusy(false)
        toast.error(`The checks could not run: ${r.error}`)
        return
      }
      setChecks(r.ok ? r.data : "unavailable")
    }
    setBusy(false)
    // An instance archive has no online dry run (there is no instance restore
    // route): straight on to the offline instructions.
    setStep(cliOnly ? 5 : 4)
  }
  const dryRun = async () => {
    setBusy(true)
    const out = ctx.demo ? dryRunFixture(ctx.scope) : await perform(false, () => restore({ ...req, dry_run: true }, restoreWs), "", "The dry run failed")
    setBusy(false)
    if (out) setReport(out)
  }
  const doRestore = async () => {
    const out = ctx.demo ? restorePhasesFixture() : await perform(false, () => restore({ ...req, dry_run: false }, restoreWs), "", "The restore failed")
    if (!out) throw new Error("restore failed")
    setOutcome(out)
    setStep(5)
  }
  const reset = () => { setStep(1); setSource(null); setChecks(null); setReport(null); setOutcome(null); setHeld(false); setChosen(null) }

  return (
    <>
      <ol className="flex flex-wrap gap-1.5" aria-label="Restore steps">
        {steps.map((label, i) => (
          <li key={label} aria-current={i + 1 === current ? "step" : undefined}
            className={cn("rounded-full border px-2.5 py-1 text-xs", i + 1 === current ? "border-primary bg-primary/[0.14] text-foreground" : i + 1 < current ? "border-border text-foreground" : "border-border text-muted-foreground")}>
            {i + 1}. {label}
          </li>
        ))}
      </ol>

      {step === 1 && offsite && (
        <OffsitePicker demo={ctx.demo} onClose={() => setOffsite(false)}
          onPicked={(p, copyScope) => { setOffsite(false); pick({ path: p, kind: copyScope === "instance" ? "instance" : "unknown" }) }} />
      )}

      {step === 1 && !offsite && (
        <SettingsCard icon={Archive} title="Pick a backup" description="Newest first; the kind of archive decides where it can go" actions={<>
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => setOffsite(true)}><Cloud />From off-site storage…</Button>
          <BackupsUpload disabled={ctx.demo} onUploaded={(receipt) => { setUploaded(receipt); runs.reload() }} />
        </>}>
          {uploaded && (
            <SettingsRow label="Archive uploaded" description={<>
              Checksum verified; contents and restore have not been checked.
              {uploaded.conversion_required && " This legacy archive requires conversion before recovery."}
              {(instance ? uploaded.scope !== "instance" : uploaded.scope === "instance") && " Switch to the archive’s scope to restore it."}
            </>}>
              {!uploaded.conversion_required && (instance ? uploaded.scope === "instance" : uploaded.scope !== "instance") &&
                <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => pick({ path: uploaded.path, kind: uploaded.scope === "crew" ? "crew" : uploaded.scope === "instance" ? "instance" : "workspace" })}>Use uploaded backup</Button>}
            </SettingsRow>
          )}
          <Gate resource={runs} what="Backups">
            {() => pool.length === 0 ? <SettingsEmpty>No backup in this scope yet.</SettingsEmpty> : (
              <div className="overflow-x-auto">
                <table className={settingsTable}>
                  <thead><tr>
                    {["Started", "Scope", "Plan", "Size", "Checked to"].map((h) => <th key={h} className={settingsTh}>{h}</th>)}
                  </tr></thead>
                  <tbody>
                    {pool.slice(0, 8).map((r) => (
                      <tr key={r.id} tabIndex={0} className={settingsTableRowLink}
                        onClick={() => pick({ path: r.bundle_path!, kind: archiveKind(r) })}
                        onKeyDown={(e) => { if (e.key === "Enter") pick({ path: r.bundle_path!, kind: archiveKind(r) }) }}>
                        <td className={settingsTd}>{formatWhen(r.started_at, now)}</td>
                        <td className={settingsTd}>{r.scope === "instance" ? "Whole instance" : r.workspace_name ?? "—"}</td>
                        <td className={settingsTd}>{runPlanLabel(r)}</td>
                        <td className={settingsTd}>{formatSize(r.size_bytes)}</td>
                        <td className={cn(settingsTd, "text-muted-foreground")}>{proofLabel(r.proof_level, r.drill_result)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Gate>
        </SettingsCard>
      )}

      {step >= 2 && picked && <p className="text-xs text-muted-foreground">From {runPlanLabel(picked)} · {formatWhen(picked.started_at, now)} · {formatSize(picked.size_bytes)}</p>}

      {step === 2 && (
        <>
          <SettingsCard icon={MapPin} title="Where to" description={KIND_NOTE[kind]}>
            <div role="radiogroup" aria-label="Restore into">
              {options.map((t) => (
                <button key={t.key} type="button" role="radio" aria-checked={target === t.key} aria-label={t.label} onClick={() => setChosen(t.key)}
                  className="flex w-full items-start gap-3 border-b border-border px-4 py-2.5 text-left last:border-b-0 hover:bg-accent">
                  <span aria-hidden className={cn("mt-0.5 h-3.5 w-3.5 shrink-0 rounded-full border", target === t.key ? "border-[4px] border-primary" : "border-control-border")} />
                  <span className="min-w-0 flex-1">
                    <span className="block text-control text-foreground">{t.label}</span>
                    <span className="mt-0.5 block text-label leading-snug text-muted-foreground">{t.about}</span>
                  </span>
                </button>
              ))}
            </div>
            {option.named && (
              <SettingsRow label={<label htmlFor="restore-as-name">{option.named === "crew" ? "Crew name" : "New workspace name"}</label>}>
                <Input id="restore-as-name" value={asName} onChange={(e) => setAsName(e.target.value)} className={settingsControl} />
              </SettingsRow>
            )}
            {kind === "custom" && (
              <SettingsRow label="Replace is not offered" description="This archive holds only some kinds of data; replacing a workspace from it would delete the rest.">{null}</SettingsRow>
            )}
          </SettingsCard>
          <div><Button size="sm" className="h-7 text-xs" onClick={() => setStep(3)} disabled={!!option.named && !asName.trim()}>Next</Button></div>
        </>
      )}

      {step === 3 && (
        <>
          <SettingsCard icon={KeyRound} title="Keys" description="Used for this restore only, never stored">
            <SettingsRow label="Backup key (AGE)" description="A key file, the key pasted, or the passphrase of an older bundle">
              <span className="flex w-full flex-col gap-1.5 sm:w-64">
                <label className="inline-flex w-fit">
                  <span className="sr-only">Choose identity file</span>
                  <input type="file" className="text-xs file:mr-2 file:h-7 file:rounded-md file:border file:border-control-border file:bg-card file:px-2.5 file:text-xs"
                    onChange={async (e) => { const f = e.target.files?.[0]; if (f) setIdentity(await f.text()) }} />
                </label>
                <Textarea aria-label="AGE identity" rows={2} value={identity} onChange={(e) => setIdentity(e.target.value)} placeholder="AGE-SECRET-KEY-1…" spellCheck={false} className="font-mono" />
                <Input aria-label="Passphrase (older bundles)" type="password" placeholder="or passphrase (older bundles)" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} className={settingsControl} />
              </span>
            </SettingsRow>
            <SettingsRow label="Vault keys" description="Unlock credentials inside the restored data">
              {kind === "instance"
                ? vault.data?.recovery_kit.enabled
                  ? <StatusPill tone="success" label={`${vault.data.versions.map((v) => v.version).join(" and ")} in the recovery kit`} />
                  : <StatusPill tone="warn" label="not in this backup · credentials must be entered again" />
                : <StatusPill tone="success" label="secrets re-wrapped for this backup" />}
            </SettingsRow>
          </SettingsCard>
          <div><Button size="sm" className="h-7 text-xs" onClick={runChecks} disabled={busy || (!ctx.demo && !identity.trim() && !passphrase)}>{busy ? "Checking…" : "Run the checks"}</Button></div>
        </>
      )}

      {step === 4 && !cliOnly && (
        <>
          <ChecksCard checks={checks} />
          {!report && <div><Button size="sm" className="h-7 text-xs" onClick={dryRun} disabled={busy}>{busy ? "Running…" : "Run the dry run"}</Button></div>}
          {report && (
            <>
              <ReportCard icon={FlaskConical} title="Dry run" report={report} />
              <div><Button size="sm" variant="destructive" className="h-7 text-xs" onClick={() => setConfirm(true)}>Restore…</Button></div>
              <ConfirmDialog open={confirm} onOpenChange={setConfirm} destructive title={target === "replace" ? "Replace the workspace?" : "Restore this backup?"}
                description="The restore runs now and can take minutes; keep this page open until it finishes."
                consequences={[
                  ...(target === "replace" ? [{ tone: "lost" as const, text: "Everything in the workspace now is replaced by the backup." }] : []),
                  ...report.warnings.map((w) => ({ tone: "warn" as const, text: w })),
                  ...(target !== "replace" ? [{ tone: "kept" as const, text: "Nothing that exists on this server is overwritten." }] : []),
                ]}
                confirmLabel="Restore" onConfirm={doRestore} />
            </>
          )}
        </>
      )}

      {step === 5 && cliOnly && !held && (
        <>
          <ChecksCard checks={checks} />
          <OfflineCard target={target as RestoreTarget} bundle={path} onNext={() => setHeld(true)} />
        </>
      )}

      {cliOnly && held && <HeldCard ctx={ctx} onReset={reset} />}

      {step === 5 && !cliOnly && outcome && (
        <ResultView outcome={outcome} demo={ctx.demo} req={req} workspaceId={outcome.restoredWorkspaceId ?? restoreWs} onReset={reset} />
      )}
    </>
  )
}

const KIND_NOTE: Record<ArchiveKind, string> = {
  instance: "An instance archive restores offline on an empty server, or opens as an isolated drill",
  workspace: "A workspace archive restores as that workspace, in place or under a new name",
  custom: "A partial archive restores only beside what exists, under a new name",
  crew: "A crew archive restores as that crew, in place or under a new name",
  unknown: "Where this backup can go is confirmed by the checks",
}

/** A report from the server: its verdict, what did not land, and notes. */
function ReportCard({ icon, title, report }: { icon: typeof Archive; title: string; report: RestoreOutcome }) {
  return (
    <SettingsCard icon={icon} title={title} description={report.summary !== title ? report.summary : undefined}
      tint={report.result === "ok" ? "var(--success)" : report.result === "partial" ? "var(--warn)" : "var(--destructive)"}
      actions={<StatusPill tone={RESULT_TONE[report.result] ?? "muted"} label={report.result} />}>
      {report.warnings.length === 0 && report.notes.length === 0 && <SettingsEmpty>Nothing to report.</SettingsEmpty>}
      {report.warnings.map((w) => <SettingsRow key={w} label={w}><StatusPill tone="warn" label="gap" /></SettingsRow>)}
      {report.notes.map((n) => <SettingsRow key={n} label={<span className="text-muted-foreground">{n}</span>}>{null}</SettingsRow>)}
    </SettingsCard>
  )
}

/**
 * After an online restore: the server's verdict as it is, then what is left
 * when the data landed but the crews' files did not (a restore under a new
 * name copies no files into containers that do not exist yet).
 */
function ResultView({ outcome, demo, req, workspaceId, onReset }: {
  outcome: RestoreOutcome
  demo: boolean
  req: { path: string; identity?: string; passphrase?: string }
  workspaceId: string | null
  onReset: () => void
}) {
  const [files, setFiles] = React.useState<"todo" | "busy" | "done">("todo")
  const pending = outcome.crewFilesPending || outcome.environmentsPending
  if ((outcome.phases ?? []).length > 0) {
    return (
      <SettingsCard icon={History} title="Restoring" description="Phases of this restore">
        {(outcome.phases ?? []).map((p, i) => (
          <SettingsRow key={p.name} label={`${i + 1} · ${p.name}`} description={p.detail ?? undefined}>
            <StatusPill tone={p.status === "done" ? "success" : p.status === "failed" ? "danger" : "muted"} label={p.status} />
          </SettingsRow>
        ))}
      </SettingsCard>
    )
  }
  const title = outcome.crewFilesPending ? "Data restored · environments still to finish"
    : outcome.result === "ok" ? "Restore finished" : outcome.result === "partial" ? "Restored with gaps" : "Restore failed"
  const bringBack = async () => {
    if (!workspaceId) { toast.error("The restored workspace is not known; bring the files back from the CLI"); return }
    setFiles("busy")
    const out = await perform(demo, () => bringBackCrewFiles(req, workspaceId), "Crew files are back", "The crew files could not be brought back")
    setFiles(out ? "done" : "todo")
  }
  return (
    <>
      <ReportCard icon={outcome.result === "ok" && !pending ? CircleCheck : TriangleAlert} title={title} report={outcome} />
      {pending && (
        <SettingsCard icon={ListTodo} title="To finish" description="The data is in; these steps bring the crews back to work">
          {outcome.crewFilesPending && (
            <>
              <SettingsRow label="Start each crew once" description="In the restored workspace, so its containers exist">
                <StatusPill tone="muted" label="to do" />
              </SettingsRow>
              <SettingsRow label="Bring back crew files" description="Copies each crew’s files into its new container, checked against this archive">
                {files === "done"
                  ? <StatusPill tone="success" label="done" />
                  : <Button size="sm" variant="outline" className="h-7 text-xs" disabled={files === "busy"} onClick={() => void bringBack()}>{files === "busy" ? "Copying…" : "Bring back crew files"}</Button>}
              </SettingsRow>
            </>
          )}
          {outcome.environmentsPending && (
            <SettingsRow label="Complete environments" description="Environments rebuilt or skipped are listed above; finish them from each crew">
              <StatusPill tone="warn" label="to check" />
            </SettingsRow>
          )}
        </SettingsCard>
      )}
      <div><Button size="sm" variant="outline" className="h-7 text-xs" onClick={onReset}>Start over</Button></div>
    </>
  )
}

/**
 * Recovery step 1 from an off-site copy: pick a destination, see the bundles
 * it holds, fetch one back (a server job, followed here) or use the copy this
 * server already has, then carry on with that bundle.
 */
function OffsitePicker({ demo, onPicked, onClose }: { demo: boolean; onPicked: (path: string, scope: "instance" | "workspace") => void; onClose: () => void }) {
  const dests = useDestinations()
  const [chosen, setChosen] = React.useState<string | null>(null)
  const destId = chosen ?? dests.data?.[0]?.id ?? null
  const copies = useOffsiteCopies(destId)
  const [fetching, setFetching] = React.useState<string | null>(null)
  const follow = React.useRef<AbortController | null>(null)
  React.useEffect(() => () => follow.current?.abort(), [])

  const fetchOne = async (key: string, scope: "instance" | "workspace") => {
    if (!destId) return
    if (demo) { toast.message("Demo data · nothing was sent"); return }
    setFetching(key)
    follow.current = new AbortController()
    const r = await fetchAndWait(destId, key, { signal: follow.current.signal })
    setFetching(null)
    if (r.ok) {
      toast.success("Fetched · the bundle is on this server now")
      onPicked(r.data, scope)
    } else if (!follow.current.signal.aborted) {
      toast.error(`The copy could not be fetched: ${r.error}`)
    }
  }

  return (
    <SettingsCard icon={Cloud} title="From off-site storage" description="Fetched back to this server, verified, then restored like any other"
      actions={<Button size="sm" variant="outline" className="h-7 text-xs" onClick={onClose}>Back to this server's backups</Button>}>
      <Gate resource={dests} what="Off-site destinations">
        {(list) => list.length === 0 ? (
          <SettingsEmpty>No off-site destination is set up. Add one in Storage.</SettingsEmpty>
        ) : (
          <>
            {list.length > 1 && (
              <div className="border-b border-border px-4 py-2.5">
                <SettingsSegmented<string> label="Destination" value={destId ?? ""} onChange={setChosen}
                  options={list.map((d) => ({ value: d.id, label: d.name }))} />
              </div>
            )}
            <Gate resource={copies} what="Off-site copies">
              {(data) => data.copies.length === 0 ? (
                <SettingsEmpty>No bundle at {data.destination_name}.</SettingsEmpty>
              ) : (
                <div className="overflow-x-auto">
                  <table className={settingsTable}>
                    <thead><tr>
                      {["Bundle", "Scope", "Size", "Copied", ""].map((h) => <th key={h} className={settingsTh}>{h}</th>)}
                    </tr></thead>
                    <tbody>
                      {data.copies.map((c) => (
                        <tr key={c.key}>
                          <td className={cn(settingsTd, "font-mono")}>{c.key}</td>
                          <td className={settingsTd}>{c.scope === "instance" ? "Whole instance" : `Workspace ${c.workspace_id ?? ""}`}</td>
                          <td className={settingsTd}>{formatSize(c.size)}</td>
                          <td className={settingsTd}>{shortDate(new Date(c.modified))}</td>
                          <td className={cn(settingsTd, "text-right")}>
                            {c.local && c.local_path
                              ? <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => onPicked(c.local_path!, c.scope)} title={`Already on this server: ${c.local_path}`}>Use</Button>
                              : <Button size="sm" className="h-7 text-xs" disabled={fetching !== null} onClick={() => void fetchOne(c.key, c.scope)}>{fetching === c.key ? "Fetching…" : "Fetch"}</Button>}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Gate>
          </>
        )}
      </Gate>
    </SettingsCard>
  )
}

function Check({ ok }: { ok: boolean }) {
  return <StatusPill tone={ok ? "success" : "danger"} label={ok ? "ok" : "no"} />
}

function ChecksCard({ checks }: { checks: RestoreChecks | "unavailable" | null }) {
  if (checks === "unavailable" || checks === null) return <Unavailable what="checks before a restore" />
  return (
    <SettingsCard icon={ListChecks} title="Before anything changes">
      <SettingsRow label="Space" description={`${formatSize(checks.space.need_bytes)} needed, ${formatSize(checks.space.free_bytes)} free`}><Check ok={checks.space.ok} /></SettingsRow>
      <SettingsRow label="Format" description={`v${checks.format.version}, ${checks.format.converter ? "through a converter; the original stays untouched" : "restorable directly"}`}><Check ok={checks.format.ok} /></SettingsRow>
      <SettingsRow label="Runtime" description={[checks.runtime.detail, ...checks.runtime.warnings].filter(Boolean).join(" · ")}><Check ok={checks.runtime.ok} /></SettingsRow>
      {checks.unsafe.length > 0 && (
        <SettingsRow label="Not carried over for safety" description={checks.unsafe.join(" · ")}><StatusPill tone="warn" label="off" /></SettingsRow>
      )}
      <SettingsRow label="Conflicts" description={checks.conflicts.detail}>{checks.conflicts.ok ? <Check ok /> : <StatusPill tone="warn" label="review" />}</SettingsRow>
    </SettingsCard>
  )
}

/**
 * Where an instance restore really happens: on the machine that holds the
 * private key. The exact command for the bundle picked, with the key file as
 * a placeholder, and a Copy button.
 */
function OfflineCard({ target, bundle, onNext }: { target: RestoreTarget; bundle: string | null; onNext: () => void }) {
  const cmd = offlineCommand(target, bundle)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(cmd)
      toast.success("Command copied")
    } catch {
      toast.error("Could not copy; select the command and copy it by hand")
    }
  }
  return (
    <SettingsCard icon={Terminal} title={target === "isolated" ? "Run the drill from the command line" : "Restore from the command line"}
      description="The server keeps no private key, so this restore runs where the key is"
      actions={<Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => void copy()}>Copy</Button>}>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all px-4 py-3 font-mono text-xs">{cmd}</pre>
      <p className="border-t border-border px-4 py-2.5 text-xs text-muted-foreground">
        Replace <code className="font-mono">{KEY_FILE_PLACEHOLDER}</code> with the file that holds the private backup key (AGE-SECRET-KEY-1…).{" "}
        {target === "isolated"
          ? "The drill restores into a throwaway directory, checks attachments, credentials, memory and the journal, and --post records the result on this server."
          : "Run it on the new server with an empty data directory, then start Crewship there; routines, webhooks and the queue stay held and container files wait in staging until you release them."}
      </p>
      <div className="border-t border-border px-4 py-2.5"><Button size="sm" variant="outline" className="h-7 text-xs" onClick={onNext}>After the restore: what is held</Button></div>
    </SettingsCard>
  )
}

/** After an offline instance restore: what stayed held until an admin resumes it. */
function HeldCard({ ctx, onReset }: { ctx: SectionCtx; onReset: () => void }) {
  const holds = useHolds()
  return (
    <>
      <SettingsCard icon={PauseCircle} title="Held after the restore" description="Nothing runs, and nothing comes in, until you say so">
        <Gate resource={holds} what="Held work">
          {(list) => list.length === 0 ? <SettingsEmpty>Nothing is held.</SettingsEmpty> : <>{list.map((h) => (
            <SettingsRow key={h.key} label={`${h.count} ${h.key === "routines" ? "routines" : h.key === "webhooks" ? "webhook endpoints" : "queued and external operations"}`} description={h.detail}>
              {h.key === "webhooks"
                ? <Button size="sm" variant="outline" className="h-7 text-xs" onClick={async () => { if (await perform(ctx.demo, () => resumeHold(h.key), "Webhooks resumed", "Could not resume")) holds.reload() }}>Resume</Button>
                : <Button size="sm" variant="outline" className="h-7 text-xs" asChild><Link href={h.key === "routines" ? "/routines" : "/inbox"}>Review…</Link></Button>}
            </SettingsRow>
          ))}</>}
        </Gate>
      </SettingsCard>
      <div><Button size="sm" variant="outline" className="h-7 text-xs" onClick={onReset}>Start over</Button></div>
    </>
  )
}

export function RestoreHistory({ now = new Date() }: { now?: Date }) {
  const res = useRestores()
  return (
    <Gate resource={res} what="Restore history">
      {(rows) => <RestoreTable rows={rows} now={now} />}
    </Gate>
  )
}

function RestoreTable({ rows, now }: { rows: RestoreRecord[]; now: Date }) {
  return (
    <SettingsCard icon={History} title="Restore history" description="Every restore and dry run on this server">
      {rows.length === 0 ? <SettingsEmpty>No restore yet.</SettingsEmpty> : (
        <div className="overflow-x-auto">
          <table className={settingsTable}>
            <thead><tr>
              {["When", "Who", "From", "Into", "Result"].map((h) => <th key={h} className={settingsTh}>{h}</th>)}
            </tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <td className={settingsTd}>{formatWhen(r.created_at, now)}</td>
                  <td className={settingsTd}>{r.actor}</td>
                  <td className={settingsTd}>{r.source_scope === "instance" ? "Whole instance" : r.source_name} · {formatSourceDate(r.source_date)}</td>
                  <td className={settingsTd}>{r.target}{r.kind === "dry_run" ? " · dry run" : ""}</td>
                  <td className={settingsTd}>
                    <span className="inline-flex items-center gap-2">
                      <StatusPill tone={RESULT_TONE[r.result] ?? "muted"} label={r.result === "ok" ? "done" : r.result} />
                      <span className="text-muted-foreground">{r.result === "partial" ? "report kept" : r.warnings ? `${r.warnings} warning${r.warnings === 1 ? "" : "s"}` : ""}</span>
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </SettingsCard>
  )
}

function Drills({ ctx }: { ctx: SectionCtx }) {
  const settings = useBackupSettings()
  const [remind, setRemind] = React.useState<"weekly" | "monthly" | "off" | null>(null)
  const value = remind ?? settings.data?.drill_reminder ?? "monthly"
  return (
    <SettingsCard icon={ShieldCheck} title="Drills" description="A test restore, by hand, into an isolated instance">
      <SettingsRow label="Remind me">
        <SettingsSegmented<"weekly" | "monthly" | "off"> label="Remind me" value={value}
          disabled={settings.status !== "ready"}
          onChange={async (v) => { setRemind(v); await perform(ctx.demo, () => saveBackupSettings({ drill_reminder: v }), "Reminder saved", "The reminder could not be saved") }}
          options={[{ value: "weekly", label: "Weekly" }, { value: "monthly", label: "Monthly" }, { value: "off", label: "Off" }]} />
      </SettingsRow>
      <SettingsRow label="How" description="Automatic drills come later, in a separate recovery environment with its own key">
        <code className="font-mono text-xs">crewship backup drill --bundle … --identity …</code>
      </SettingsRow>
      <SettingsRow label="During a drill" description="no message is sent, no webhook called, no model run paid for; restored credentials are unusable inside the drill">{null}</SettingsRow>
      <SettingsRow label="Passes when" description="attachments open · memory loads · credentials unlock · the journal verifies · routines resume only by hand">{null}</SettingsRow>
    </SettingsCard>
  )
}
