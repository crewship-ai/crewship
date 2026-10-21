"use client"

import * as React from "react"
import { Bot, Check, Copy, FileText, Globe, Plus, Webhook, Workflow } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { WizardSteps } from "@/components/ui/wizard-steps"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { SettingsSegmented } from "@/components/features/settings/shared"
import { usePage } from "@/hooks/use-pages"
import { usePageWebhookCreate } from "@/hooks/use-page-sharing"
import type { WebhookSaveBody } from "@/hooks/use-pipeline-webhooks"
import { cn } from "@/lib/utils"
import { IncomingTargetAvatar } from "./incoming-target-avatar"
import type { IncomingKind, IncomingTarget } from "./incoming-model"
import { incomingJSON, type IncomingData } from "./use-incoming-endpoints"
import { absolute, copy } from "./views/incoming-credentials"
import { IncomingError } from "./views/incoming-shared"
import { MappingEditor, mappingProblem, mappingTemplate, type MappingRow } from "./trigger-mapping"

/**
 * New trigger (Harbor 2 v67 `inepnew`): Goes to → Sender and mapping → URL.
 *
 * Only a webhook can start work today: an app event (Composio has no receiver
 * yet) and email in are not drawn. The URL and the secret are shown once, on
 * the last step, which only closes through Done (or the header's ×) — never
 * Esc or a click outside.
 */

const STEPS = ["Goes to", "Sender and mapping", "URL"]
const DEFAULT_RATE = 60

const KINDS: { kind: IncomingKind; label: string; icon: LucideIcon; blurb: string; field: string }[] = [
  { kind: "routine", label: "Routine", icon: Workflow, blurb: "Runs a routine; the call fills its inputs", field: "Routine" },
  { kind: "agent", label: "Agent", icon: Bot, blurb: "Wakes an agent; the call is its task", field: "Agent" },
  { kind: "page", label: "Artifact", icon: FileText, blurb: "Writes rows to one panel of an artifact", field: "Artifact" },
]

type Sender = "github" | "crewship"
const SENDERS: { value: Sender; label: string; blurb: string }[] = [
  { value: "github", label: "GitHub", blurb: "Checks GitHub's signature; only pull_request events" },
  { value: "crewship", label: "Any system", blurb: "Signed with a shared secret: Stripe, Zendesk, your scripts" },
]

interface Reveal {
  url: string
  secret?: string
  target: IncomingTarget
}

export function NewTriggerDialog({
  workspaceId,
  data,
  initialTarget,
  onClose,
  onCreated,
}: {
  workspaceId: string
  data: IncomingData
  initialTarget?: IncomingTarget
  onClose: () => void
  onCreated: (t: IncomingTarget) => void
}) {
  const ids = React.useId()
  const [step, setStep] = React.useState(0)
  const [kind, setKind] = React.useState<IncomingKind>(initialTarget?.kind ?? "routine")
  const [targetId, setTargetId] = React.useState(initialTarget?.id ?? "")
  const [pin, setPin] = React.useState<"pin" | "latest">("pin")
  const [panel, setPanel] = React.useState("")
  const [sender, setSender] = React.useState<Sender>("crewship")
  const [rows, setRows] = React.useState<MappingRow[]>([])
  const [rate, setRate] = React.useState(String(DEFAULT_RATE))
  const [name, setName] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [reveal, setReveal] = React.useState<Reveal | null>(null)

  const target = data.targets.find((t) => t.kind === kind && t.id === targetId)
  const page = usePage(kind === "page" ? workspaceId : null, kind === "page" ? (target?.slug ?? null) : null)
  const panelIds = (page.page?.panels ?? []).filter((p) => p.producer?.startsWith("webhook/") || p.producer?.startsWith("script/")).map((p) => p.spec.id)
  const pageCreate = usePageWebhookCreate(workspaceId, target?.slug ?? "", {
    onOk: (w) => {
      if (target && w.url) {
        setReveal({ url: absolute(w.url), target })
        setStep(2)
      } else setRefusal("The trigger was created, but the server did not return its URL.")
    },
    onRefused: setRefusal,
  })

  const agentBlock = kind === "agent" && target ? (!target.crew_id ? "Assign this agent to a crew before it gets a trigger." : target.webhook_secret_set !== false ? "This agent already has a trigger. Open it to rotate the secret." : null) : null
  const goesToReady = !!target && (kind !== "page" || panelIds.includes(panel)) && agentBlock === null
  const problem = kind === "routine" ? mappingProblem(rows) : null
  const rateValue = Number(rate)
  const rateOk = kind !== "routine" || (Number.isInteger(rateValue) && rateValue >= 1)
  const ready = step === 0 ? goesToReady : step === 1 ? goesToReady && problem === null && rateOk : true
  const dirty = !reveal && (step > 0 || name.trim() !== "" || rows.length > 0)

  const finish = () => {
    if (reveal) onCreated(reveal.target)
    onClose()
  }

  const create = async () => {
    if (!target || busy || pageCreate.isPending) return
    setRefusal(null)
    setBusy(true)
    try {
      if (kind === "page") {
        await pageCreate.mutateAsync({ panel, name: name.trim() || undefined })
        return
      }
      if (kind === "agent") {
        const r = await incomingJSON<{ webhook_secret: string }>(`/api/v1/agents/${target.id}/webhook-secret/rotate?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
        setReveal({ target, secret: r.webhook_secret, url: absolute(`/api/v1/webhooks/${target.crew_id}/${target.id}/trigger`) })
        data.refresh()
        setStep(2)
        return
      }
      const template = mappingTemplate(rows)
      const body: WebhookSaveBody = {
        name: name.trim() || `${target.name} webhook`,
        target_pipeline_id: target.id,
        ...(pin === "pin" && target.version ? { target_pipeline_version: target.version } : {}),
        ingress_profile: sender,
        ...(Object.keys(template).length > 0 ? { inputs_template: template } : {}),
        rate_limit_per_min: rateValue,
        enabled: true,
      }
      const r = await data.hooks.create(body)
      if (!r) throw new Error("No trigger was returned")
      setReveal({ target, secret: r.signing_secret, url: absolute(`/api/v1/webhooks/${r.token}${sender === "github" ? "/github-pull-request" : ""}`) })
      setStep(2)
    } catch (e) {
      setRefusal(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const next = () => {
    if (reveal) return finish()
    if (!ready) return
    if (step === 0) setStep(1)
    else void create()
  }

  const chooseKind = (k: IncomingKind) => {
    if (k === kind) return
    setKind(k)
    setTargetId("")
    setPanel("")
    setRows([])
    if (k !== "routine") setSender("crewship")
  }

  let body: React.ReactNode
  if (reveal) {
    body = <TriggerReveal reveal={reveal} />
  } else if (step === 0) {
    const meta = KINDS.find((k) => k.kind === kind)!
    body = (
      <div className="flex flex-col gap-4">
        <CreateSurfaceField label="Started by" hint="For a time schedule, open the routine; schedules live there.">
          <SettingsSegmented label="Started by" options={[{ value: "webhook", label: "A webhook" }]} value="webhook" onChange={() => {}} />
        </CreateSurfaceField>
        <div role="radiogroup" aria-label="Goes to" className="grid gap-1.5 sm:grid-cols-3">
          {KINDS.map((k) => (
            <Choice key={k.kind} label={k.label} blurb={k.blurb} icon={<k.icon className="size-5 shrink-0 text-muted-foreground" aria-hidden />} on={kind === k.kind} onPick={() => chooseKind(k.kind)} />
          ))}
        </div>
        <CreateSurfaceField label={meta.field}>
          <Select
            value={targetId}
            onValueChange={(v) => {
              setTargetId(v)
              setPanel("")
            }}
          >
            <SelectTrigger aria-label={meta.field}>
              <SelectValue placeholder={`Choose ${kind === "agent" ? "an agent" : kind === "page" ? "an artifact" : "a routine"}`} />
            </SelectTrigger>
            <SelectContent>
              {data.targets
                .filter((t) => t.kind === kind)
                .map((t) => (
                  <SelectItem key={t.id} value={t.id}>
                    <span className="flex items-center gap-2">
                      <IncomingTargetAvatar target={t} />
                      {t.name}
                    </span>
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        </CreateSurfaceField>
        {kind === "routine" && target?.version ? (
          <CreateSurfaceField label="Version" hint="A pinned version keeps a routine edit from changing what the call does.">
            <SettingsSegmented
              label="Version"
              options={[
                { value: "pin", label: `Pin v${target.version}` },
                { value: "latest", label: "Always latest" },
              ]}
              value={pin}
              onChange={setPin}
            />
          </CreateSurfaceField>
        ) : null}
        {kind === "page" && target && (
          <CreateSurfaceField label="Artifact panel" hint="Only panels with a webhook or script producer accept these writes.">
            {page.loading ? (
              <Skeleton className="h-9" />
            ) : (
              <Select value={panel} onValueChange={setPanel}>
                <SelectTrigger aria-label="Artifact panel">
                  <SelectValue placeholder="Choose a panel" />
                </SelectTrigger>
                <SelectContent>
                  {panelIds.map((id) => (
                    <SelectItem key={id} value={id}>
                      {id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            {page.error && <IncomingError message={page.error} />}
          </CreateSurfaceField>
        )}
        {agentBlock && <p className="text-label text-muted-foreground">{agentBlock}</p>}
        {data.error && <IncomingError message={data.error} onRetry={data.refresh} />}
      </div>
    )
  } else {
    body = (
      <div className="flex flex-col gap-4">
        {kind === "page" ? (
          <p className="text-body text-muted-foreground">Artifact webhooks use a token in the URL. Only a person can create one; it writes this panel and nothing else, and it can never do more than you can.</p>
        ) : (
          <CreateSurfaceField label="Who sends it">
            <div role="radiogroup" aria-label="Who sends it" className="grid gap-1.5 sm:grid-cols-2">
              {SENDERS.filter((s) => kind === "routine" || s.value === "crewship").map((s) => (
                <Choice
                  key={s.value}
                  label={s.label}
                  blurb={s.blurb}
                  icon={s.value === "github" ? <Webhook className="size-5 shrink-0 text-muted-foreground" aria-hidden /> : <Globe className="size-5 shrink-0 text-muted-foreground" aria-hidden />}
                  on={sender === s.value}
                  onPick={() => setSender(s.value)}
                />
              ))}
            </div>
          </CreateSurfaceField>
        )}
        {kind === "agent" && <p className="text-label text-muted-foreground">The call becomes the task of a background run. Up to 60 calls a minute.</p>}
        {kind === "routine" && (
          <CreateSurfaceField label="Map the call to inputs" hint="Each routine input takes a value from the call, like {{ inputs.event.pull_request.number }}. event, raw and headers are always passed.">
            <MappingEditor rows={rows} onChange={setRows} problem={problem} />
          </CreateSurfaceField>
        )}
        {kind === "routine" && (
          <CreateSurfaceField label="Calls per minute" htmlFor={`${ids}-rate`} hint="More are refused with 429; the sender retries.">
            <Input id={`${ids}-rate`} type="number" min={1} value={rate} onChange={(e) => setRate(e.target.value)} className={cn(CREATE_SURFACE_INPUT, "w-28")} />
          </CreateSurfaceField>
        )}
        {kind !== "agent" && (
          <CreateSurfaceField label="Name" htmlFor={`${ids}-name`}>
            <Input id={`${ids}-name`} value={name} onChange={(e) => setName(e.target.value)} placeholder={target ? `${target.name} webhook` : "github-prs"} className={CREATE_SURFACE_INPUT} />
          </CreateSurfaceField>
        )}
      </div>
    )
  }

  return (
    <CreateSurface
      open
      onOpenChange={(open) => {
        // The URL and secret are shown once: Esc or a click outside must not
        // throw them away. Done or the header's × closes on purpose.
        if (!open && !reveal) onClose()
      }}
      size="lg"
      dirty={dirty}
      discardLabel="this trigger"
      onSubmit={next}
    >
      <CreateSurfaceHeader concept="integrations" context="Integrations" title="New trigger" description="Something outside that starts work. Pick what it starts first." onClose={finish} />
      <div className="shrink-0 border-b border-hairline px-4 py-2 sm:px-5">
        <WizardSteps steps={STEPS} current={step} onStepChange={reveal ? undefined : (i) => setStep(i)} />
      </div>
      <CreateSurfaceBody>{body}</CreateSurfaceBody>
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      {reveal ? (
        <CreateSurfaceFooter hint={`Step 3 of 3`} onCancel={finish} guardCancel={false} cancelLabel="Close" primaryLabel="Done" primaryIcon={Check} onPrimary={finish} />
      ) : step === 0 ? (
        <CreateSurfaceFooter hint="Step 1 of 3" onCancel={onClose} guardCancel primaryLabel="Continue" primaryDisabled={!ready} onPrimary={next} />
      ) : (
        <CreateSurfaceFooter
          hint="Step 2 of 3"
          onCancel={() => setStep(0)}
          guardCancel={false}
          cancelLabel="Back"
          primaryLabel="Create"
          primaryIcon={Plus}
          primaryDisabled={!ready}
          busy={busy || pageCreate.isPending}
          onPrimary={next}
        />
      )}
    </CreateSurface>
  )
}

function Choice({ label, blurb, icon, on, onPick }: { label: string; blurb: string; icon: React.ReactNode; on: boolean; onPick: () => void }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={on}
      aria-label={label}
      onClick={onPick}
      className={cn("kit-tap flex min-w-0 items-start gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors", on ? "border-primary bg-primary/10" : "border-border bg-card hover:border-line-strong")}
    >
      {icon}
      <span className="flex min-w-0 flex-1 flex-col">
        <b className="truncate text-body font-semibold text-foreground">{label}</b>
        <small className="text-label text-muted-foreground">{blurb}</small>
      </span>
      {on && <Check className="size-4 shrink-0 text-primary" aria-hidden />}
    </button>
  )
}

function TriggerReveal({ reveal }: { reveal: Reveal }) {
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-1 rounded-lg border border-warn/40 bg-chip-warn-bg px-3 py-3">
        <b className="text-body font-semibold text-chip-warn-fg">Copy the URL{reveal.secret ? " and the secret" : ""} now</b>
        <small className="text-label text-muted-foreground">{reveal.secret ? "The secret is shown once. Paste both at the sender." : "The URL holds the token and is shown once."}</small>
      </div>
      <CreateSurfaceField label="URL" hint="Stays the same if you later point it at another routine.">
        <div className="flex min-w-0 items-center gap-2">
          <code data-testid="incoming-receiving-url" className="min-w-0 flex-1 break-all rounded-lg border border-border bg-card px-3 py-2 font-mono text-label text-foreground">
            {reveal.url}
          </code>
          <Button type="button" variant="outline" size="sm" aria-label="Copy URL" onClick={() => void copy(reveal.url)}>
            <Copy className="size-3.5" aria-hidden />
            Copy
          </Button>
        </div>
      </CreateSurfaceField>
      {reveal.secret && (
        <CreateSurfaceField label="Secret">
          <div className="flex min-w-0 items-center gap-2">
            <code className="min-w-0 flex-1 break-all rounded-lg border border-border bg-card px-3 py-2 font-mono text-label text-foreground">{reveal.secret}</code>
            <Button type="button" variant="outline" size="sm" aria-label="Copy secret" onClick={() => void copy(reveal.secret!)}>
              <Copy className="size-3.5" aria-hidden />
              Copy
            </Button>
          </div>
        </CreateSurfaceField>
      )}
    </div>
  )
}
