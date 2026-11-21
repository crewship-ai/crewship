"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { Checkbox } from "@/components/ui/checkbox"
import { Combobox } from "@/components/ui/combobox"
import { Input } from "@/components/ui/input"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { SettingsSegmented, controlHeight } from "@/components/features/settings/shared"
import { manifestFixture } from "./__fixtures__/backups"
import {
  CATEGORIES, formatWhen, manualRunBody, scopeLabel,
  type BackupPlan, type BackupRun, type BundleManifest, type CategoryKey, type ManualRunForm, type ScopeWorkspace,
} from "./backups-model"
import { inspectBundle, runNow } from "./use-backup-runs"
import { perform } from "./use-backups-data"
import type { SectionCtx } from "./backups-scope"

/** One labelled control of a dialog, with the sentence that explains it. */
function Field({ label, hint, children, htmlFor }: { label: string; hint?: React.ReactNode; children: React.ReactNode; htmlFor?: string }) {
  return (
    <div className="flex flex-col gap-1.5 py-2.5 [&:not(:last-child)]:border-b [&:not(:last-child)]:border-border">
      <label htmlFor={htmlFor} className="text-control font-medium text-foreground">{label}</label>
      <div>{children}</div>
      {hint && <p className="text-label text-muted-foreground">{hint}</p>}
    </div>
  )
}

/**
 * How many runs a Back up now will start: a whole-instance backup is one, a
 * workspaces scope queues one run per workspace (the server's rule).
 */
export function runsToStart(scope: SectionCtx["scope"], selected: Set<string>): number {
  return scope === "instance" ? 1 : selected.size
}

type ContentsChoice = ManualRunForm["contents"]
type EnvChoice = ManualRunForm["env"]

const PLAN_NONE = "none"

/**
 * Back up now, from the top bar: what (the scope shown in the panel), like
 * which plan, and the choices the run endpoint accepts: contents,
 * environments, encrypt to the plan's keys or to a passphrase used once, and
 * a note. It says how many runs will start before it starts them.
 */
export function BackUpNowDialog({ open, onOpenChange, ctx, plans }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  ctx: Pick<SectionCtx, "scope" | "selected" | "workspaces" | "demo">
  plans: BackupPlan[]
}) {
  const [planId, setPlanId] = React.useState<string>(PLAN_NONE)
  const [contents, setContents] = React.useState<ContentsChoice>("plan")
  const [env, setEnv] = React.useState<EnvChoice>("plan")
  const [encrypt, setEncrypt] = React.useState<"plan" | "passphrase">("plan")
  const [passphrase, setPassphrase] = React.useState("")
  const [note, setNote] = React.useState("")
  const [kinds, setKinds] = React.useState<Set<CategoryKey>>(() => new Set())
  const [busy, setBusy] = React.useState(false)

  // A fresh dialog each time it opens; the first plan that covers the scope is the likely one.
  React.useEffect(() => {
    if (!open) return
    const first = plans.find((p) => p.enabled && p.scope === ctx.scope) ?? null
    setPlanId(first?.id ?? PLAN_NONE)
    setContents("plan"); setEnv("plan"); setEncrypt("plan"); setPassphrase(""); setNote(""); setKinds(new Set()); setBusy(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reset on open only
  }, [open])

  const plan = plans.find((p) => p.id === planId) ?? null
  const instance = ctx.scope === "instance"
  const count = runsToStart(ctx.scope, ctx.selected)
  const choice: ContentsChoice = plan || contents !== "plan" ? contents : "full"
  const body = manualRunBody({
    scope: ctx.scope, plan, workspaceIds: [...ctx.selected], contents: choice === "plan" && !plan ? "full" : choice,
    categories: [...kinds], env, encrypt, passphrase, note,
  })
  const blocked = busy || (encrypt === "passphrase" && !passphrase) || (choice === "some" && kinds.size === 0) || count === 0

  const start = async () => {
    if (blocked) return
    setBusy(true)
    const out = await perform(ctx.demo, () => runNow(body), count === 1 ? "Backup started · it appears in Runs" : `${count} backups started · they appear in Runs`, "The backup could not start")
    setBusy(false)
    if (out || ctx.demo) onOpenChange(false)
  }

  const planOptions = [{ value: PLAN_NONE, label: "No plan" }, ...plans.map((p) => ({ value: p.id, label: p.name }))]
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" onSubmit={() => void start()} dirty={!!note || !!passphrase}>
      <CreateSurfaceHeader title="Back up now" description="Starts at once and runs beside the plans." onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody>
        <Field label="What" hint={instance ? "Everything, as one backup" : count === 1 ? "One run" : "One run per workspace"}>
          <span className="text-control" data-slot="backup-what">{scopeLabel(ctx.scope, ctx.selected, ctx.workspaces as ScopeWorkspace[])}</span>
          <span className="ml-2 text-label text-muted-foreground">change it with Showing in the panel</span>
        </Field>
        <Field label="Like plan" hint="Takes its contents, keys and destinations" htmlFor="backup-like-plan">
          <Combobox id="backup-like-plan" ariaLabel="Like plan" value={planId} onChange={setPlanId} options={planOptions} modal />
        </Field>
        <Field label="Contents" hint={instance ? "A whole-instance backup is always a full backup" : undefined}>
          <SettingsSegmented<ContentsChoice> label="Contents" value={choice} onChange={setContents}
            options={[...(plan ? [{ value: "plan" as const, label: "As the plan" }] : []), { value: "full", label: "Full backup" }, ...(instance ? [] : [{ value: "some" as const, label: "Only some kinds" }])]} />
          {choice === "some" && (
            <div className="mt-2 grid gap-1.5 sm:grid-cols-2" role="group" aria-label="Kinds to back up">
              {CATEGORIES.filter((c) => c.key !== "env").map((c) => (
                <label key={c.key} className="flex items-center gap-2 text-control coarse:min-h-[44px]">
                  <Checkbox checked={kinds.has(c.key)} onCheckedChange={(v) => setKinds((k) => { const n = new Set(k); if (v === true) n.add(c.key); else n.delete(c.key); return n })} />
                  {c.label}
                </label>
              ))}
              <p className="text-label text-warn sm:col-span-2">A partial backup does not count as protection: it cannot bring a workspace back by itself.</p>
            </div>
          )}
        </Field>
        <Field label="Environments">
          <SettingsSegmented<EnvChoice> label="Environments" value={env} onChange={setEnv}
            options={[...(plan ? [{ value: "plan" as const, label: "As the plan" }] : []), { value: "files", label: "Files only" }, { value: "complete", label: "Complete" }]} />
        </Field>
        <Field label="Encrypt to" hint={encrypt === "passphrase" ? "Used once for this run and never stored. Nobody else can open it without it." : "Every backup is encrypted."}>
          <SettingsSegmented<"plan" | "passphrase"> label="Encrypt to" value={encrypt} onChange={setEncrypt}
            options={[{ value: "plan", label: "The plan’s keys" }, { value: "passphrase", label: "A passphrase" }]} />
          {encrypt === "passphrase" && (
            <Input aria-label="Passphrase" type="password" autoComplete="new-password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} className={cn(controlHeight, "mt-2")} />
          )}
        </Field>
        <Field label="Note (optional)" htmlFor="backup-note">
          <Input id="backup-note" placeholder="e.g. before the upgrade" value={note} onChange={(e) => setNote(e.target.value)} className={controlHeight} maxLength={200} />
        </Field>
        <p className="pt-2 text-control text-foreground" role="status" data-slot="run-count">
          {count === 1 ? "1 run will start." : `${count} runs will start, one per workspace.`}
        </p>
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel={busy ? "Starting…" : count > 1 ? `Back up ${count} workspaces` : "Back up now"}
        onPrimary={() => void start()} primaryDisabled={blocked} busy={busy} />
    </CreateSurface>
  )
}

/** What a backup holds, read from its plaintext manifest (nothing is decrypted). */
export function ManifestDialog({ run, demo, onClose }: { run: BackupRun | null; demo: boolean; onClose: () => void }) {
  const [state, setState] = React.useState<{ status: "loading" | "ready" | "error"; manifest?: BundleManifest; error?: string }>({ status: "loading" })
  React.useEffect(() => {
    if (!run?.bundle_path) return
    let live = true
    setState({ status: "loading" })
    if (demo) {
      setState({ status: "ready", manifest: manifestFixture(run) })
      return
    }
    void inspectBundle(run.bundle_path).then((r) => {
      if (!live) return
      if (r.ok) setState({ status: "ready", manifest: r.data })
      else setState({ status: "error", error: r.unavailable ? "Not available on this server yet." : r.error })
    })
    return () => { live = false }
  }, [run, demo])
  const m = state.manifest
  const c = m?.contents
  const rows: [string, React.ReactNode][] = m ? [
    ["Format", `v${m.format_version}${m.crewship_version_at_backup ? ` · Crewship ${m.crewship_version_at_backup}` : ""}`],
    ["Covers", m.scope === "instance" ? "The whole instance" : m.scope === "crew" ? "One crew" : c?.workspace?.name ?? "One workspace"],
    ["Made", `${formatWhen(m.created_at)}${m.created_by?.email ? ` by ${m.created_by.email}` : ""}`],
    ...(c?.crews ? [["Crews", String(c.crews.length)] as [string, React.ReactNode]] : []),
    ...(m.kind === "custom" ? [["Kinds", `Only ${(m.categories ?? []).join(", ") || "some kinds"}: a partial backup does not protect a workspace by itself`] as [string, React.ReactNode]] : []),
    ...(c?.attachments_included != null ? [["Attachment files", `${c.attachments_included} in it${c.attachments_missing ? `, ${c.attachments_missing} missing` : ""}`] as [string, React.ReactNode]] : []),
    ...(c?.memory_blobs_included != null ? [["Memory files", `${c.memory_blobs_included} in it${c.memory_blobs_missing ? `, ${c.memory_blobs_missing} missing` : ""}`] as [string, React.ReactNode]] : []),
    ...(c?.missing_container_crews?.length ? [["Crews without files", c.missing_container_crews.join(", ")] as [string, React.ReactNode]] : []),
    ...(m.scope === "instance" ? [["Instance parts", [c?.instance_config_included && "settings", c?.credstore_included && "credentials", c?.auth_keys_included && "sign-in keys"].filter(Boolean).join(", ") || "database"] as [string, React.ReactNode]] : []),
    ["Encryption", m.encryption?.enabled === false ? "Not encrypted" : "Encrypted (AGE)"],
  ] : []
  return (
    <CreateSurface open={!!run} onOpenChange={(o) => { if (!o) onClose() }} size="sm">
      <CreateSurfaceHeader title="What is inside" description="Read from the manifest, which is not encrypted. Nothing is decrypted or restored." onClose={onClose} />
      <CreateSurfaceBody>
        {state.status === "loading" && <p className="text-label text-muted-foreground">Reading the manifest…</p>}
        {state.status === "error" && <p role="alert" className="text-label text-destructive">The manifest could not be read: {state.error}</p>}
        {m && (
          <dl className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)] gap-x-3 gap-y-2 text-control" data-slot="manifest">
            {rows.map(([k, v]) => (<React.Fragment key={k}><dt className="text-muted-foreground">{k}</dt><dd className="min-w-0 break-words">{v}</dd></React.Fragment>))}
          </dl>
        )}
      </CreateSurfaceBody>
      <CreateSurfaceFooter hint={null} primaryLabel="Close" onPrimary={onClose} />
    </CreateSurface>
  )
}
