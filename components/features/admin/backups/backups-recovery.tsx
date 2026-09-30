"use client"

import * as React from "react"
import Link from "next/link"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { SettingsCard, SettingsSegmented } from "@/components/features/settings/shared"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { Chip, FieldRow, Gate, ItemRow, SmallButton, TD, TH, Unavailable, WsName } from "./backups-kit"
import {
  formatSize, formatWhen, proofLabel, runPlanLabel, shortDate,
  type RestoreChecks, type RestoreRecord, type RestoreReport, type RestoreTarget,
} from "./backups-model"
import { checksFixture, dryRunFixture, restorePhasesFixture } from "./__fixtures__/backups"
import { useBackupRuns, workspaceFor } from "./use-backup-runs"
import { fetchAndWait, restore, restoreChecks, resumeHold, useHolds, useOffsiteCopies, useRestores } from "./use-backup-recovery"
import { saveBackupSettings, useBackupSettings, useDestinations, useVaultKeys } from "./use-backup-settings"
import { perform } from "./use-backups-data"
import type { SectionCtx } from "./backups-console"

type Sub = "new" | "history" | "drills"
const STEPS = ["Backup", "Target", "Keys", "Checks", "Dry run", "Restore", "Resume"] as const
// An instance target restores offline, where the key is: after the checks the
// wizard shows the command, and there is no online dry run or restore step.
const CLI_STEPS: { n: number; label: string }[] = [
  { n: 1, label: "Backup" }, { n: 2, label: "Target" }, { n: 3, label: "Keys" }, { n: 4, label: "Checks" }, { n: 5, label: "Command line" }, { n: 7, label: "Resume" },
]

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
 * Backups › Recovery: a guided restore — pick a backup, a target, the keys;
 * checks before anything changes, a dry run with its full report, the restore
 * in phases, then what stayed held until an admin resumes it. Plus every
 * restore so far, and how drills are run.
 */
export function BackupsRecovery({ ctx }: { ctx: SectionCtx }) {
  const [sub, setSub] = React.useState<Sub>("new")
  return (
    <>
      <SettingsSegmented<Sub> label="Recovery" value={sub} onChange={setSub}
        options={[{ value: "new", label: "New restore" }, { value: "history", label: "History" }, { value: "drills", label: "Drills" }]} />
      {sub === "new" && <RestoreWizard ctx={ctx} />}
      {sub === "history" && <RestoreHistory />}
      {sub === "drills" && <Drills ctx={ctx} />}
    </>
  )
}

const TARGETS: Record<"instance" | "workspaces", { key: RestoreTarget; label: string; about: string }[]> = {
  instance: [
    { key: "empty_server", label: "Empty server", about: "The whole instance. From the CLI or the first-boot screen, since this page does not exist on a new server." },
    { key: "isolated", label: "Isolated instance", about: "The same restore into a throwaway instance, to prove the backup." },
  ],
  workspaces: [
    { key: "replace", label: "Replace a workspace", about: "Everything in it is replaced." },
    { key: "new_workspace", label: "Into a new workspace", about: "A copy beside the original, identities remapped." },
    { key: "crew", label: "One crew", about: "Into an existing workspace." },
  ],
}

export function RestoreWizard({ ctx, now = new Date() }: { ctx: SectionCtx; now?: Date }) {
  const runs = useBackupRuns(ctx.scope, ctx.selected, ctx.workspaces)
  const vault = useVaultKeys()
  const [step, setStep] = React.useState(ctx.focusPath ? 2 : 1)
  const [path, setPath] = React.useState<string | null>(ctx.focusPath)
  const [target, setTarget] = React.useState<RestoreTarget>(ctx.scope === "instance" ? "empty_server" : "new_workspace")
  const [asName, setAsName] = React.useState("")
  const [identity, setIdentity] = React.useState("")
  const [passphrase, setPassphrase] = React.useState("")
  const [checks, setChecks] = React.useState<RestoreChecks | "unavailable" | null>(null)
  const [report, setReport] = React.useState<RestoreReport | null>(null)
  const [progress, setProgress] = React.useState<RestoreReport | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [confirm, setConfirm] = React.useState(false)
  const [offsite, setOffsite] = React.useState(false)
  const instance = ctx.scope === "instance"
  const cliOnly = target === "empty_server" || target === "isolated"
  const key = { identity: identity.trim() || undefined, passphrase: passphrase || undefined }
  const req = { path: path ?? "", target, ...key, as_workspace: target === "new_workspace" ? asName : undefined, as_crew: target === "crew" ? asName : undefined }
  const pool = (runs.data ?? []).filter((r) => r.bundle_path && (runs.source === "legacy" || (instance ? r.scope === "instance" : r.scope === "workspaces")))
  const picked = pool.find((r) => r.bundle_path === path) ?? null
  // The workspace a workspace/crew restore acts on, named explicitly.
  const restoreWs = workspaceFor(ctx, picked?.workspace_id)

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
    // An instance target has no online dry run (there is no instance restore
    // route): straight on to the offline instructions.
    setStep(cliOnly ? 5 : 4)
  }
  const dryRun = async () => {
    setBusy(true)
    const out = ctx.demo ? dryRunFixture(ctx.scope) : await perform(false, () => restore({ ...req, dry_run: true }, restoreWs), "", "The dry run failed")
    setBusy(false)
    if (out) { setReport(out); setStep(5) }
  }
  const doRestore = async () => {
    const out = ctx.demo ? restorePhasesFixture() : await perform(false, () => restore({ ...req, dry_run: false }, restoreWs), "Restore finished", "The restore failed")
    if (!out) throw new Error("restore failed")
    setProgress(out)
    setStep(6)
  }
  const reset = () => { setStep(1); setPath(null); setChecks(null); setReport(null); setProgress(null) }

  return (
    <>
      <ol className="flex flex-wrap gap-1.5" aria-label="Restore steps">
        {(cliOnly ? CLI_STEPS : STEPS.map((label, i) => ({ n: i + 1, label }))).map((s, i) => (
          <li key={s.label} aria-current={s.n === step ? "step" : undefined}
            className={cn("rounded-full border px-2.5 py-1 text-[12.5px]", s.n === step ? "border-primary bg-primary/[0.14] text-foreground" : "border-border text-muted-foreground")}>
            {i + 1}. {s.label}
          </li>
        ))}
      </ol>

      {step === 1 && offsite && (
        <OffsitePicker demo={ctx.demo} onClose={() => setOffsite(false)}
          onPicked={(p) => { setPath(p); setOffsite(false); setStep(2) }} />
      )}

      {step === 1 && !offsite && (
        <SettingsCard title="Pick a backup" actions={<>
          <SmallButton onClick={() => setOffsite(true)}>From off-site storage…</SmallButton>
          <SmallButton disabled title="Uploading a backup from another machine comes later">Upload a backup…</SmallButton>
        </>}>
          <Gate resource={runs} what="Backups">
            {() => pool.length === 0 ? <p className="px-4 py-3 text-[12.5px] text-muted-foreground">No backup in this scope yet.</p> : (
              <div className="overflow-x-auto">
                <table className="w-full tabular-nums">
                  <tbody>
                    {pool.slice(0, 8).map((r) => (
                      <tr key={r.id} tabIndex={0} className="cursor-pointer hover:[&>td]:bg-muted [&:last-child>td]:border-b-0"
                        onClick={() => { setPath(r.bundle_path); setStep(2) }}
                        onKeyDown={(e) => { if (e.key === "Enter") { setPath(r.bundle_path); setStep(2) } }}>
                        <td className={TD}>{formatWhen(r.started_at, now)}</td>
                        <td className={TD}>{r.scope === "instance" ? <WsName name="Whole instance" instance /> : <WsName name={r.workspace_name ?? "—"} />}</td>
                        <td className={TD}>{runPlanLabel(r)}</td>
                        <td className={TD}>{formatSize(r.size_bytes)}</td>
                        <td className={TD}>{proofLabel(r.proof_level, r.drill_result)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Gate>
        </SettingsCard>
      )}

      {step >= 2 && picked && step < 7 && <p className="text-[12.5px] text-muted-foreground">From {runPlanLabel(picked)} · {formatWhen(picked.started_at, now)} · {formatSize(picked.size_bytes)}</p>}

      {step === 2 && (
        <>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3" role="radiogroup" aria-label="Restore into">
            {TARGETS[instance ? "instance" : "workspaces"].map((t) => (
              <button key={t.key} type="button" role="radio" aria-checked={target === t.key} onClick={() => setTarget(t.key)}
                className={cn("flex flex-col gap-1 rounded-[10px] border bg-card px-3 py-2.5 text-left", target === t.key ? "border-primary bg-primary/[0.08]" : "border-border")}>
                <b className="text-[13px] font-semibold">{t.label}</b>
                <small className="text-[12.5px] text-muted-foreground">{t.about}{t.key === "empty_server" && <> <code className="font-mono text-[11.5px]">crewship recover --bundle … --identity ops-2026.key</code></>}</small>
              </button>
            ))}
          </div>
          {(target === "new_workspace" || target === "crew") && (
            <label className="flex max-w-sm flex-col gap-1 text-[13px]">
              <span className="text-muted-foreground">{target === "crew" ? "Crew name" : "New workspace name"}</span>
              <input value={asName} onChange={(e) => setAsName(e.target.value)} className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 coarse:h-[2.75rem]" />
            </label>
          )}
          <div><SmallButton primary onClick={() => setStep(3)} disabled={(target === "new_workspace" || target === "crew") && !asName.trim()}>Next</SmallButton></div>
        </>
      )}

      {step === 3 && (
        <>
          <SettingsCard title="Keys">
            <FieldRow label="Backup key (AGE)" detail="used for this restore only, never stored">
              <span className="flex flex-col gap-1.5">
                <label className="inline-flex w-fit">
                  <span className="sr-only">Choose identity file</span>
                  <input type="file" className="text-[12px] file:mr-2 file:h-7 file:rounded-md file:border file:border-control-border file:bg-card file:px-2.5 file:text-xs"
                    onChange={async (e) => { const f = e.target.files?.[0]; if (f) setIdentity(await f.text()) }} />
                </label>
                <textarea aria-label="AGE identity" rows={2} value={identity} onChange={(e) => setIdentity(e.target.value)} placeholder="AGE-SECRET-KEY-1…" spellCheck={false}
                  className="rounded-md border border-control-border bg-surface-subtle px-2 py-1.5 font-mono text-[12px]" />
                <input aria-label="Passphrase (older bundles)" type="password" placeholder="or passphrase (older bundles)" value={passphrase} onChange={(e) => setPassphrase(e.target.value)}
                  className="h-8 rounded-md border border-control-border bg-surface-subtle px-2 text-[13px] coarse:h-[2.75rem]" />
              </span>
            </FieldRow>
            <FieldRow label="Vault keys">
              {instance
                ? vault.data?.recovery_kit.enabled
                  ? <Chip tone="ok">✓ {vault.data.versions.map((v) => v.version).join(" and ")} in the recovery kit</Chip>
                  : <Chip tone="warn">not in this backup · credentials must be entered again</Chip>
                : <Chip tone="ok">✓ secrets re-wrapped for this backup</Chip>}
            </FieldRow>
          </SettingsCard>
          <div><SmallButton primary onClick={runChecks} disabled={busy || (!ctx.demo && !identity.trim() && !passphrase)}>{busy ? "Checking…" : "Run the checks"}</SmallButton></div>
        </>
      )}

      {step === 4 && (
        <>
          <ChecksCard checks={checks} />
          <div><SmallButton primary onClick={dryRun} disabled={busy}>{busy ? "Running…" : "Run the dry run"}</SmallButton></div>
        </>
      )}

      {step === 5 && cliOnly && (
        <>
          <ChecksCard checks={checks} />
          <OfflineCard target={target} bundle={path} onNext={() => setStep(7)} />
        </>
      )}

      {step === 5 && !cliOnly && report && (
        <>
          <SettingsCard title="Dry run" actions={<Chip tone={report.result === "ok" ? "ok" : report.result === "partial" ? "warn" : "bad"}>{report.result}</Chip>}>
            <ul className="m-0 list-disc py-3 pl-8 pr-4 text-[13px] [&>li]:my-1">
              <li>{report.summary}</li>
              {report.warnings.map((w) => <li key={w}><Chip tone="warn">!</Chip> {w}</li>)}
              {report.notes.map((n) => <li key={n}>{n}</li>)}
            </ul>
          </SettingsCard>
          <div><SmallButton danger onClick={() => setConfirm(true)}>Restore…</SmallButton></div>
          <ConfirmDialog open={confirm} onOpenChange={setConfirm} destructive title={target === "replace" ? "Replace the workspace?" : "Restore this backup?"}
            description="The restore runs in phases and keeps its progress across a restart of this server."
            consequences={[
              ...(target === "replace" ? [{ tone: "lost" as const, text: "Everything in the workspace now is replaced by the backup." }] : []),
              ...report.warnings.map((w) => ({ tone: "warn" as const, text: w })),
              { tone: "kept" as const, text: "Routines, webhooks, queues and external retries stay held until you resume them." },
            ]}
            confirmLabel="Restore" onConfirm={doRestore} />
        </>
      )}

      {step === 6 && progress && (
        <>
          <SettingsCard title="Restoring" description="phases, kept across a restart of this server">
            {(progress.phases ?? []).length === 0 ? (
              <FieldRow label="Result"><Chip tone={progress.result === "ok" ? "ok" : "warn"}>{progress.result}</Chip> {progress.summary}</FieldRow>
            ) : (progress.phases ?? []).map((p, i) => (
              <FieldRow key={p.name} label={`${i + 1} · ${p.name}`}>
                {p.status === "done" ? <Chip tone="ok">done</Chip> : p.status === "failed" ? <Chip tone="bad">failed</Chip> : p.status === "running" ? <Chip tone="muted">running</Chip> : null} {p.detail}
              </FieldRow>
            ))}
          </SettingsCard>
          <div><SmallButton primary onClick={() => setStep(7)}>Next</SmallButton></div>
        </>
      )}

      {step === 7 && <HeldCard ctx={ctx} onReset={reset} />}
    </>
  )
}

/**
 * Recovery step 1 from an off-site copy: pick a destination, see the bundles
 * it holds, fetch one back (a server job, followed here) or use the copy this
 * server already has, then carry on with that bundle.
 */
function OffsitePicker({ demo, onPicked, onClose }: { demo: boolean; onPicked: (path: string) => void; onClose: () => void }) {
  const dests = useDestinations()
  const [chosen, setChosen] = React.useState<string | null>(null)
  const destId = chosen ?? dests.data?.[0]?.id ?? null
  const copies = useOffsiteCopies(destId)
  const [fetching, setFetching] = React.useState<string | null>(null)
  const follow = React.useRef<AbortController | null>(null)
  React.useEffect(() => () => follow.current?.abort(), [])

  const fetchOne = async (key: string) => {
    if (!destId) return
    if (demo) { toast.message("Demo data · nothing was sent"); return }
    setFetching(key)
    follow.current = new AbortController()
    const r = await fetchAndWait(destId, key, { signal: follow.current.signal })
    setFetching(null)
    if (r.ok) {
      toast.success("Fetched · the bundle is on this server now")
      onPicked(r.data)
    } else if (!follow.current.signal.aborted) {
      toast.error(`The copy could not be fetched: ${r.error}`)
    }
  }

  return (
    <SettingsCard title="From off-site storage" description="a bundle is fetched back to this server, verified, then restored like any other"
      actions={<SmallButton onClick={onClose}>Back to this server's backups</SmallButton>}>
      <Gate resource={dests} what="Off-site destinations">
        {(list) => list.length === 0 ? (
          <p className="px-4 py-3 text-[12.5px] text-muted-foreground">No off-site destination is set up. Add one in Storage.</p>
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
                <p className="px-4 py-3 text-[12.5px] text-muted-foreground">No bundle at {data.destination_name}.</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full tabular-nums">
                    <thead><tr>{["Bundle", "Scope", "Size", "Copied", ""].map((h) => <th key={h} className={TH}>{h}</th>)}</tr></thead>
                    <tbody>
                      {data.copies.map((c) => (
                        <tr key={c.key} className="[&:last-child>td]:border-b-0">
                          <td className={cn(TD, "font-mono text-[12px]")}>{c.key}</td>
                          <td className={TD}>{c.scope === "instance" ? "Whole instance" : `Workspace ${c.workspace_id ?? ""}`}</td>
                          <td className={TD}>{formatSize(c.size)}</td>
                          <td className={TD}>{shortDate(new Date(c.modified))}</td>
                          <td className={cn(TD, "text-right")}>
                            {c.local && c.local_path
                              ? <SmallButton onClick={() => onPicked(c.local_path!)} title={`Already on this server: ${c.local_path}`}>Use</SmallButton>
                              : <SmallButton primary disabled={fetching !== null} onClick={() => void fetchOne(c.key)}>{fetching === c.key ? "Fetching…" : "Fetch"}</SmallButton>}
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

function ChecksCard({ checks }: { checks: RestoreChecks | "unavailable" | null }) {
  if (checks === "unavailable" || checks === null) return <Unavailable what="checks before a restore" />
  return (
    <SettingsCard title="Before anything changes">
      <FieldRow label="Space"><Chip tone={checks.space.ok ? "ok" : "bad"}>{checks.space.ok ? "✓" : "✗"}</Chip> {formatSize(checks.space.need_bytes)} needed, {formatSize(checks.space.free_bytes)} free</FieldRow>
      <FieldRow label="Format" detail="an older backup goes through a converter first; the original stays untouched">
        <Chip tone={checks.format.ok ? "ok" : "bad"}>{checks.format.ok ? "✓" : "✗"}</Chip> v{checks.format.version}, {checks.format.converter ? "through a converter" : "restorable directly"}
      </FieldRow>
      <FieldRow label="Runtime" detail={checks.runtime.warnings.length ? checks.runtime.warnings.join(" · ") : undefined} detailTone="warn">
        <Chip tone={checks.runtime.ok ? "ok" : "bad"}>{checks.runtime.ok ? "✓" : "✗"}</Chip> {checks.runtime.detail}
      </FieldRow>
      {checks.unsafe.length > 0 && (
        <FieldRow label="Not carried over for safety" detail="restored switched off; turn back on by hand after review">{checks.unsafe.join(" · ")}</FieldRow>
      )}
      <FieldRow label="Conflicts">{checks.conflicts.ok && <><Chip tone="ok">✓</Chip> </>}{checks.conflicts.detail}</FieldRow>
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
    <SettingsCard title={target === "isolated" ? "Run the drill from the command line" : "Restore from the command line"}
      description="the server keeps no private key, so this restore runs where the key is"
      actions={<SmallButton onClick={() => void copy()}>Copy</SmallButton>}>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all px-4 py-3 font-mono text-[12px]">{cmd}</pre>
      <p className="border-t border-border px-4 py-2.5 text-[12.5px] text-muted-foreground">
        Replace <code className="font-mono">{KEY_FILE_PLACEHOLDER}</code> with the file that holds the private backup key (AGE-SECRET-KEY-1…).{" "}
        {target === "isolated"
          ? "The drill restores into a throwaway directory, checks attachments, credentials, memory and the journal, and --post records the result on this server."
          : "Run it on the new server with an empty data directory, then start Crewship there; what was held stays held until you resume it."}
      </p>
      <div className="border-t border-border px-4 py-2.5"><SmallButton onClick={onNext}>After the restore: what is held</SmallButton></div>
    </SettingsCard>
  )
}

function HeldCard({ ctx, onReset }: { ctx: SectionCtx; onReset: () => void }) {
  const holds = useHolds()
  return (
    <>
      <SettingsCard title="Held after the restore" description="nothing runs, and nothing comes in, until you say so">
        <Gate resource={holds} what="Held work">
          {(list) => list.length === 0 ? <p className="px-4 py-3 text-[12.5px] text-success">Nothing is held.</p> : <>{list.map((h) => (
            <ItemRow key={h.key} title={`${h.count} ${h.key === "routines" ? "routines" : h.key === "webhooks" ? "webhook endpoints" : "queued and external operations"}`} detail={h.detail}
              action={h.key === "webhooks"
                ? <SmallButton onClick={async () => { if (await perform(ctx.demo, () => resumeHold(h.key), "Webhooks resumed", "Could not resume")) holds.reload() }}>Resume</SmallButton>
                : <SmallButton asChild><Link href={h.key === "routines" ? "/routines" : "/inbox"}>Review…</Link></SmallButton>} />
          ))}</>}
        </Gate>
      </SettingsCard>
      <div><SmallButton onClick={onReset}>Start over</SmallButton></div>
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
    <div className="overflow-hidden rounded-card border border-border bg-card">
      <div className="overflow-x-auto">
        <table className="w-full tabular-nums">
          <thead><tr>{["When", "Who", "From", "Into", "Result"].map((h) => <th key={h} className={TH}>{h}</th>)}</tr></thead>
          <tbody>
            {rows.length === 0 && <tr><td colSpan={5} className="px-4 py-4 text-center text-[12.5px] text-muted-foreground">No restore yet.</td></tr>}
            {rows.map((r) => (
              <tr key={r.id} className="[&:last-child>td]:border-b-0">
                <td className={TD}>{formatWhen(r.created_at, now)}</td>
                <td className={TD}>{r.actor}</td>
                <td className={TD}><WsName name={r.source_name} instance={r.source_scope === "instance"} /> · {shortDate(new Date(r.source_date))}</td>
                <td className={TD}>{r.target}{r.kind === "dry_run" ? " · dry run" : ""}</td>
                <td className={TD}>
                  <Chip tone={r.result === "ok" ? "ok" : r.result === "partial" ? "warn" : "bad"}>{r.result === "ok" ? "done" : r.result}</Chip>{" "}
                  <span className="text-muted-foreground">{r.result === "partial" ? "report kept" : r.warnings ? `${r.warnings} warning${r.warnings === 1 ? "" : "s"}` : ""}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function Drills({ ctx }: { ctx: SectionCtx }) {
  const settings = useBackupSettings()
  const [remind, setRemind] = React.useState<"weekly" | "monthly" | "off" | null>(null)
  const value = remind ?? settings.data?.drill_reminder ?? "monthly"
  return (
    <SettingsCard title="Drills" description="a test restore, by hand, into an isolated instance">
      <FieldRow label="Remind me">
        <SettingsSegmented<"weekly" | "monthly" | "off"> label="Remind me" value={value}
          disabled={settings.status !== "ready"}
          onChange={async (v) => { setRemind(v); await perform(ctx.demo, () => saveBackupSettings({ drill_reminder: v }), "Reminder saved", "The reminder could not be saved") }}
          options={[{ value: "weekly", label: "Weekly" }, { value: "monthly", label: "Monthly" }, { value: "off", label: "Off" }]} />
      </FieldRow>
      <FieldRow label="How" detail="automatic drills later, in a separate recovery environment with its own key">
        I run a test restore into an isolated instance with my key: <code className="font-mono text-[12px]">crewship backup drill --bundle … --identity …</code>
      </FieldRow>
      <FieldRow label="During a drill" detail="restored credentials are unusable inside the drill">no message is sent, no webhook called, no model run paid for</FieldRow>
      <FieldRow label="Passes when">attachments open · memory loads · credentials unlock · the journal verifies · routines resume only by hand</FieldRow>
    </SettingsCard>
  )
}

