"use client"

import * as React from "react"
import { Check, Copy, Download, KeyRound, Loader2, ShieldCheck, Sparkles } from "lucide-react"
import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { useSessionSafe } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { SettingsCard, controlHeight } from "@/components/features/settings/shared"
import { ageKeyFile, canGenerateAgeKey, generateAgeKey, type AgeKeyPair } from "./age-key"
import { newDraft } from "./backups-schedules"
import { savePlan } from "./use-backup-plans"
import { runNow } from "./use-backup-runs"
import { addRecipient } from "./use-backup-settings"
import { perform } from "./use-backups-data"
import type { BackupPlan, BackupRecipient } from "./backups-model"

/**
 * What a first backup needs, and whether it is there: a backup key (every
 * backup is encrypted to one), a plan that runs on its own, and one backup
 * that ran. The Overview leads with it until all three are done, and Back up
 * now opens the setup instead of failing while there is no key.
 */
export function setupState(keys: BackupRecipient[] | null, plans: BackupPlan[] | null) {
  const hasKey = (keys?.length ?? 0) > 0
  const hasPlan = (plans ?? []).some(recoversInstance)
  return { known: keys !== null && plans !== null, hasKey, hasPlan, done: hasKey && hasPlan }
}

/**
 * A plan that can bring the whole server back: enabled, the whole instance,
 * everything in it. A plan for some workspaces, or for part of the data, is
 * a fine plan but not this one — and a run with its plan_id takes the plan's
 * scope, whatever the request says.
 */
export function recoversInstance(p: BackupPlan): boolean {
  return p.enabled && p.scope === "instance" && p.preset === "complete"
}

/** The card at the top of the Overview while backups are not set up. */
export function SetupCard({ hasKey, hasPlan, onStart }: { hasKey: boolean; hasPlan: boolean; onStart: () => void }) {
  const step = (done: boolean, n: number, label: string, hint: string) => (
    <li className="flex items-start gap-3 border-b border-border px-4 py-2.5 last:border-b-0" data-step-done={done}>
      <span className={cn("grid h-5 w-5 shrink-0 place-items-center rounded-full font-mono text-micro",
        done ? "bg-success/15 text-success" : "bg-muted text-muted-foreground")} aria-hidden>
        {done ? <Check className="h-3 w-3" strokeWidth={3} /> : n}
      </span>
      <span className="min-w-0 flex-1">
        <span className={cn("block text-control", done && "text-muted-foreground line-through")}>{label}</span>
        <span className="block text-label text-muted-foreground">{hint}</span>
      </span>
    </li>
  )
  return (
    <SettingsCard icon={Sparkles} tint="var(--primary)" title="Set up backups"
      description="Three steps, a few minutes. Until then nothing on this server is backed up."
      actions={<Button type="button" size="sm" className="h-7 px-3 text-xs" onClick={onStart}>Set up backups</Button>}>
      <ol data-slot="backups-setup-card">
        {step(hasKey, 1, "A backup key", "Every backup is encrypted to it. Made in your browser; you keep the private half.")}
        {step(hasPlan, 2, "A plan for the whole instance", "The whole instance every night, kept for a year, on this server.")}
        {step(false, 3, "The first backup", "Run it now and watch it in Runs.")}
      </ol>
    </SettingsCard>
  )
}

type Step = "key" | "plan" | "run" | "done"

/**
 * Set up backups, one step at a time: a key made here (or pasted), the plan
 * every server needs, and the first backup. Steps already done are skipped.
 */
export function BackupsSetup({ open, onOpenChange, keys, plans, demo, onChanged, onOpenRuns }: {
  open: boolean
  onOpenChange: (o: boolean) => void
  keys: BackupRecipient[]
  plans: BackupPlan[]
  demo: boolean
  /** Reload keys and plans after a step saved something. */
  onChanged: () => void
  onOpenRuns: () => void
}) {
  const state = setupState(keys, plans)
  const first: Step = !state.hasKey ? "key" : !state.hasPlan ? "plan" : "run"
  const [step, setStep] = React.useState<Step>(first)
  const [keyIds, setKeyIds] = React.useState<string[]>([])
  const [planId, setPlanId] = React.useState<string | null>(null)
  React.useEffect(() => {
    if (!open) return
    setStep(first)
    setKeyIds([])
    setPlanId(null)
  }, [open]) // eslint-disable-line react-hooks/exhaustive-deps -- start from current server state once per opening
  const steps: { key: Step; label: string }[] = [{ key: "key", label: "1. Key" }, { key: "plan", label: "2. Plan" }, { key: "run", label: "3. First backup" }]
  const order: Step[] = ["key", "plan", "run", "done"]

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <div className="flex items-start gap-3">
            <span className="icon-tile inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-xl" style={{ "--ic": "var(--primary)" } as React.CSSProperties} aria-hidden>
              <ShieldCheck className="h-5 w-5" />
            </span>
            <div>
              <DialogTitle>Set up backups</DialogTitle>
              <DialogDescription>A key to encrypt to, a plan that runs on its own, and the first backup.</DialogDescription>
            </div>
          </div>
        </DialogHeader>
        <ol className="flex flex-wrap gap-1.5" aria-label="Steps">
          {steps.map((s) => {
            const done = order.indexOf(step) > order.indexOf(s.key) || (s.key === "key" && state.hasKey) || (s.key === "plan" && state.hasPlan)
            return (
              <li key={s.key} aria-current={step === s.key ? "step" : undefined}
                className={cn("inline-flex h-7 items-center gap-1 rounded-full border px-2.5 text-xs",
                  step === s.key ? "border-primary bg-primary/10 text-primary-hover" : done ? "border-border text-success" : "border-border text-muted-foreground")}>
                {done && step !== s.key && <Check className="h-3 w-3" />}{s.label}
              </li>
            )
          })}
        </ol>
        {step === "key" && (
          <KeyStep demo={demo} onSaved={(id) => { setKeyIds([id]); onChanged(); setStep(state.hasPlan ? "run" : "plan") }} />
        )}
        {step === "plan" && (
          <PlanStep demo={demo} recipientIds={keyIds.length ? keyIds : keys.map((k) => k.id)}
            onSaved={(id) => { setPlanId(id); onChanged(); setStep("run") }} />
        )}
        {step === "run" && (
          <RunStep demo={demo} planId={planId ?? plans.find(recoversInstance)?.id ?? null}
            onStarted={() => setStep("done")} onSkip={() => onOpenChange(false)} />
        )}
        {step === "done" && (
          <div className="flex flex-col gap-3">
            <p className="flex items-center gap-2 text-control text-success"><Check className="h-4 w-4" />The first backup is running. Backups are set up.</p>
            <p className="text-label text-muted-foreground">Next: add off-site storage (Storage) so losing this server does not lose its backups, and test a restore once (Restore › Drills).</p>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" size="sm" onClick={() => onOpenChange(false)}>Close</Button>
              <Button type="button" size="sm" onClick={() => { onOpenChange(false); onOpenRuns() }}>Watch it in Runs</Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function KeyStep({ demo, onSaved }: { demo: boolean; onSaved: (id: string) => void }) {
  const { data: session } = useSessionSafe()
  const [mode, setMode] = React.useState<"make" | "paste">(canGenerateAgeKey() ? "make" : "paste")
  const [key, setKey] = React.useState<AgeKeyPair | null>(null)
  const [pasted, setPasted] = React.useState("")
  const [holder, setHolder] = React.useState(session?.user.name ?? "")
  const [kept, setKept] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const name = `Backup key ${new Date().toISOString().slice(0, 10)}`
  const publicKey = mode === "make" ? key?.recipient ?? "" : pasted.trim()
  const valid = /^age1[02-9ac-hj-np-z]{58}$/.test(publicKey) && (mode === "paste" || kept)

  const make = async () => {
    try { setKey(await generateAgeKey()); setKept(false) } catch (e) {
      toast.error(`This browser could not make a key: ${e instanceof Error ? e.message : "unknown error"}. Paste one made with age-keygen instead.`)
      setMode("paste")
    }
  }
  const download = () => {
    if (!key) return
    const url = URL.createObjectURL(new Blob([ageKeyFile(key)], { type: "text/plain" }))
    const a = document.createElement("a")
    a.href = url; a.download = "crewship-backup-key.txt"
    document.body.appendChild(a); a.click(); a.remove()
    URL.revokeObjectURL(url)
  }
  const save = async () => {
    setBusy(true)
    const out = await perform(demo, () => addRecipient({ name, public_key: publicKey, holder: holder.trim() }), "Backup key added", "The key could not be added")
    setBusy(false)
    if (out) onSaved(out.id)
  }

  const choice = (m: "make" | "paste", title: string, hint: string, disabled = false) => (
    <button type="button" role="radio" aria-checked={mode === m} disabled={disabled} onClick={() => setMode(m)}
      className={cn("flex-1 rounded-lg border px-3 py-2.5 text-left transition-colors disabled:opacity-50",
        mode === m ? "border-primary bg-primary/5" : "border-border hover:bg-accent")}>
      <span className="block text-control font-medium">{title}</span>
      <span className="block text-label text-muted-foreground">{hint}</span>
    </button>
  )

  return (
    <div className="flex flex-col gap-3" data-slot="setup-key">
      <p className="text-label text-muted-foreground">
        Every backup is encrypted to a key. The server keeps only its public half; the private half — which reads the backups — stays with you. Lose it and no backup can be restored.
      </p>
      <div role="radiogroup" aria-label="Backup key" className="flex flex-col gap-2 sm:flex-row">
        {choice("make", "Make one here", "In this browser; the private half never reaches the server", !canGenerateAgeKey())}
        {choice("paste", "I have one", "Paste a public key from age-keygen (age1…)")}
      </div>
      {mode === "make" && !key && (
        <Button type="button" size="sm" className="self-start" onClick={() => void make()}><KeyRound />Make the key</Button>
      )}
      {mode === "make" && key && (
        <div className="flex flex-col gap-2 rounded-lg border border-warn/40 bg-warn/5 p-3">
          <span className="text-label font-medium text-warn">The private key — shown once. Keep it off this server.</span>
          <code className="break-all rounded-md bg-surface-subtle px-2 py-1.5 font-mono text-micro" data-slot="private-key">{key.identity}</code>
          <div className="flex flex-wrap gap-1.5">
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs" onClick={download}><Download />Download key file</Button>
            <Button type="button" size="sm" variant="outline" className="h-7 text-xs"
              onClick={() => { void navigator.clipboard?.writeText(key.identity).then(() => toast.success("Private key copied")) }}><Copy />Copy</Button>
          </div>
          <span className="text-label text-muted-foreground">Public key, which the server keeps: <span className="break-all font-mono">{key.recipient}</span></span>
          <label className="flex items-start gap-2 text-label">
            <input type="checkbox" checked={kept} onChange={(e) => setKept(e.target.checked)} className="mt-0.5 size-3.5 accent-[var(--primary)]" />
            I stored the private key somewhere safe, off this server (a password manager, a printed recovery sheet).
          </label>
        </div>
      )}
      {mode === "paste" && (
        <Input aria-label="Public key" placeholder="age1…" value={pasted} onChange={(e) => setPasted(e.target.value)} spellCheck={false} className={cn(controlHeight, "font-mono")} />
      )}
      <Input aria-label="Who holds the private key" placeholder="Who holds the private key" value={holder} onChange={(e) => setHolder(e.target.value)} className={controlHeight} />
      <div className="flex justify-end">
        <Button type="button" size="sm" disabled={!valid || busy} onClick={() => void save()}>{busy && <Loader2 className="animate-spin" />}Save the key and continue</Button>
      </div>
    </div>
  )
}

function PlanStep({ demo, recipientIds, onSaved }: { demo: boolean; recipientIds: string[]; onSaved: (id: string) => void }) {
  const [time, setTime] = React.useState("03:00")
  const [busy, setBusy] = React.useState(false)
  const draft = newDraft("instance", new Set())
  const save = async () => {
    setBusy(true)
    const out = await perform(demo, () => savePlan({ ...draft, time_of_day: time, recipient_ids: recipientIds }), "Plan created · it runs on its own", "The plan could not be created")
    setBusy(false)
    if (out) onSaved(out.id)
  }
  const row = (label: string, value: React.ReactNode) => (
    <div className="flex items-center gap-3 border-b border-border px-3 py-2 last:border-b-0">
      <span className="w-28 shrink-0 text-label text-muted-foreground">{label}</span><span className="min-w-0 flex-1 text-control">{value}</span>
    </div>
  )
  return (
    <div className="flex flex-col gap-3" data-slot="setup-plan">
      <p className="text-label text-muted-foreground">The plan every server needs: everything, every night. You can change any of it later in its plan.</p>
      <div className="rounded-lg border border-border">
        {row("What", "Complete recovery · the whole instance, with container environments weekly")}
        {row("When", <span className="inline-flex items-center gap-2">every night at
          <Input type="time" aria-label="Time" value={time} onChange={(e) => setTime(e.target.value)} className="h-7 w-28" />
          <span className="text-label text-muted-foreground">{draft.timezone}</span></span>)}
        {row("Kept", "7 daily, 4 weekly and 12 monthly copies")}
        {row("Where", "This server — add off-site storage afterwards")}
        {row("Encrypted to", `${recipientIds.length} backup key${recipientIds.length === 1 ? "" : "s"}`)}
      </div>
      <div className="flex justify-end">
        <Button type="button" size="sm" disabled={busy || recipientIds.length === 0} onClick={() => void save()}>{busy && <Loader2 className="animate-spin" />}Create the plan</Button>
      </div>
    </div>
  )
}

function RunStep({ demo, planId, onStarted, onSkip }: { demo: boolean; planId: string | null; onStarted: () => void; onSkip: () => void }) {
  const [busy, setBusy] = React.useState(false)
  const run = async () => {
    setBusy(true)
    const out = await perform(demo, () => runNow({ plan_id: planId ?? undefined, scope: "instance", preset: "complete" }), "Backup started", "The backup could not start")
    setBusy(false)
    if (out) onStarted()
  }
  return (
    <div className="flex flex-col gap-3" data-slot="setup-run">
      <p className="text-label text-muted-foreground">Run the plan once now, rather than waiting for tonight: a backup that has run is one you can check and restore.</p>
      <div className="flex justify-end gap-2">
        <Button type="button" size="sm" variant="outline" onClick={onSkip}>Tonight is fine</Button>
        <Button type="button" size="sm" disabled={busy} onClick={() => void run()}>{busy && <Loader2 className="animate-spin" />}Back up now</Button>
      </div>
    </div>
  )
}
