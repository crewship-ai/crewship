"use client"

import * as React from "react"
import { Check, Copy, Eye, Mail, Plus, Send, Webhook } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
  CreateSurfaceSteps,
} from "@/components/layout/create-surface"
import { SettingsSegmented } from "@/components/features/settings/shared"
import { cn } from "@/lib/utils"
import { NOTIFICATION_CATEGORY_GROUPS } from "@/lib/notification-categories"
import { ProviderForm } from "./provider-form"
import { ProviderMark } from "./provider-marks"
import type {
  ChannelCreateBody,
  ChannelDraftTestBody,
  CreatedChannel,
  NotificationProvider,
} from "@/hooks/use-notification-channels"

/**
 * Add channel (Harbor 2, round 58): App → Where it posts → What it sends, with
 * a preview of a message on the right. Add webhook is the same dialog from
 * step two with the webhook transport chosen.
 *
 * Only services the server offers AND has turned on (Admin › Notifications)
 * are listed. A webhook's signing secret is shown once after saving; that state
 * keeps the footer, with Done, so it can only be left on purpose.
 *
 * Not drawn, because the server has none: a "Name in Crewship" (a channel is
 * named by its destination) and replying to agents from a chat app.
 */

export type AddChannelMode = "channel" | "webhook"

type Kind = { kind: "shoutrrr"; provider: string; label: string } | { kind: "email"; label: string } | { kind: "webhook"; label: string }
type Priority = "low" | "medium" | "high" | "urgent"

const STEPS = [
  { id: "app", label: "App" },
  { id: "where", label: "Where it posts" },
  { id: "what", label: "What it sends" },
]
const GROUPS: { key: string; label: string }[] = [
  { key: "chat", label: "Chat apps" },
  { key: "email", label: "Email" },
  { key: "push", label: "Push" },
  { key: "incident", label: "Incident" },
]
const PRIORITIES: { value: Priority; label: string }[] = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Normal" },
  { value: "high", label: "High" },
  { value: "urgent", label: "Urgent" },
]

interface AddChannelDialogProps {
  /** null = closed. */
  mode: AddChannelMode | null
  onClose: () => void
  providers: NotificationProvider[]
  /** false = this visitor may only create personal channels. */
  canCreateWorkspace: boolean
  create: (body: ChannelCreateBody) => Promise<CreatedChannel | null>
  sendDraftTest: (body: ChannelDraftTestBody) => Promise<unknown>
  /** The new channel's id, so the host can open its page. */
  onCreated?: (id: string, webhook: boolean) => void
}

export function AddChannelDialog({ mode, onClose, providers, canCreateWorkspace, create, sendDraftTest, onCreated }: AddChannelDialogProps) {
  if (!mode) return null
  return (
    <AddChannelSurface
      key={mode}
      mode={mode}
      onClose={onClose}
      providers={providers}
      canCreateWorkspace={canCreateWorkspace}
      create={create}
      sendDraftTest={sendDraftTest}
      onCreated={onCreated}
    />
  )
}

function AddChannelSurface({ mode, onClose, providers, canCreateWorkspace, create, sendDraftTest, onCreated }: AddChannelDialogProps & { mode: AddChannelMode }) {
  const offered = React.useMemo(() => providers.filter((p) => p.enabled), [providers])
  const [step, setStep] = React.useState(mode === "webhook" ? 1 : 0)
  const [kind, setKind] = React.useState<Kind | null>(mode === "webhook" ? { kind: "webhook", label: "Webhook" } : null)
  const [fields, setFields] = React.useState<Record<string, string>>({})
  const [destination, setDestination] = React.useState("")
  const [secret, setSecret] = React.useState("")
  const [personal, setPersonal] = React.useState(!canCreateWorkspace)
  const [categories, setCategories] = React.useState<string[]>([])
  const [minPriority, setMinPriority] = React.useState<Priority>("low")
  const [creating, setCreating] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const [tested, setTested] = React.useState<{ ok: boolean; message: string } | null>(null)
  const [revealed, setRevealed] = React.useState<string | null>(null)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const ids = React.useId()
  const webhook = kind?.kind === "webhook"
  const spec = kind?.kind === "shoutrrr" ? offered.find((p) => p.provider === kind.provider) : undefined

  const pick = (next: Kind) => {
    setKind(next)
    setFields({})
    setDestination("")
    setSecret("")
    setTested(null)
  }

  const whereReady = kind
    ? kind.kind === "shoutrrr"
      ? spec !== undefined && spec.fields.every((f) => !f.required || (fields[f.key] ?? "").trim() !== "")
      : destination.trim() !== ""
    : false
  const ready = step === 0 ? kind !== null : step === 1 ? whereReady : whereReady

  // Only typed or ticked input counts; after the reveal the channel is saved.
  const dirty =
    revealed === null &&
    (Object.values(fields).some((v) => v.trim() !== "") || destination.trim() !== "" || secret.trim() !== "" || categories.length > 0 || minPriority !== "low")

  const draft = (): ChannelDraftTestBody =>
    kind?.kind === "shoutrrr"
      ? { type: "shoutrrr", provider: kind.provider, fields }
      : kind?.kind === "webhook"
        ? { type: "webhook", url: destination.trim(), ...(secret.trim() ? { secret: secret.trim() } : {}) }
        : { type: "email", to: destination.trim() }

  const testDraft = async () => {
    setTesting(true)
    setTested(null)
    try {
      await sendDraftTest(draft())
      setTested({ ok: true, message: "Test delivered" })
    } catch (e) {
      setTested({ ok: false, message: e instanceof Error ? e.message : "Test send failed" })
    } finally {
      setTesting(false)
    }
  }

  const submit = async () => {
    if (!kind || !whereReady || creating) return
    setCreating(true)
    setRefusal(null)
    try {
      const created = await create({
        ...draft(),
        personal,
        ...(!personal && categories.length > 0 ? { categories } : {}),
        ...(minPriority !== "low" ? { min_priority: minPriority } : {}),
      })
      toast.success(`${kind.label} connected`)
      if (created?.id) onCreated?.(created.id, webhook)
      if (created?.secret && webhook) setRevealed(created.secret)
      else onClose()
    } catch (e) {
      const message = e instanceof Error ? e.message : "The channel was not added"
      setRefusal(message)
      toast.error(message)
    } finally {
      setCreating(false)
    }
  }

  const next = () => {
    if (!ready) return
    if (step < 2) setStep(step + 1)
    else void submit()
  }

  const toggle = (key: string) => setCategories((prev) => (prev.includes(key) ? prev.filter((c) => c !== key) : [...prev, key]))

  let body: React.ReactNode
  if (revealed) {
    body = <SecretReveal secret={revealed} />
  } else if (step === 0) {
    body = (
      <div className="flex flex-col gap-4">
        {GROUPS.map((g) => {
          const options: Kind[] =
            g.key === "email"
              ? [{ kind: "email", label: "Email" }]
              : offered.filter((p) => (p.category ?? "chat") === g.key).map((p) => ({ kind: "shoutrrr", provider: p.provider, label: p.label }))
          if (options.length === 0) return null
          return (
            <section key={g.key} aria-labelledby={`${ids}-${g.key}`} className="flex flex-col gap-1.5">
              <h3 id={`${ids}-${g.key}`} className="text-label font-medium text-muted-foreground">{g.label}</h3>
              <div role="radiogroup" aria-labelledby={`${ids}-${g.key}`} className="grid gap-1.5 sm:grid-cols-2">
                {options.map((o) => {
                  const key = o.kind === "shoutrrr" ? o.provider : o.kind
                  const on = kind !== null && (kind.kind === "shoutrrr" ? kind.provider : kind.kind) === key
                  const blurb = o.kind === "shoutrrr" ? offered.find((p) => p.provider === o.provider)?.blurb : "Address or list"
                  return (
                    <button
                      key={key}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      onClick={() => pick(o)}
                      className={cn("kit-tap flex min-w-0 items-center gap-2.5 rounded-lg border px-3 py-2 text-left transition-colors", on ? "border-primary bg-primary/10" : "border-border bg-card hover:border-line-strong")}
                    >
                      {o.kind === "email" ? <Mail className="size-5 shrink-0 text-muted-foreground" aria-hidden /> : <ProviderMark provider={key} label={o.label} className="size-7" />}
                      <span className="flex min-w-0 flex-1 flex-col">
                        <b className="truncate text-body font-semibold text-foreground">{o.label}</b>
                        {blurb && <small className="truncate text-label text-muted-foreground">{blurb}</small>}
                      </span>
                      {on && <Check className="size-4 shrink-0 text-primary" aria-hidden />}
                    </button>
                  )
                })}
              </div>
            </section>
          )
        })}
        <p className="flex items-center gap-1.5 text-label text-muted-foreground">
          <Webhook className="size-3.5" aria-hidden />
          Sending signed JSON to your own systems? That is a
          <button type="button" className="font-medium text-primary-hover hover:underline" onClick={() => { pick({ kind: "webhook", label: "Webhook" }); setStep(1) }}>
            Webhook
          </button>
          , not a channel.
        </p>
      </div>
    )
  } else if (step === 1 && kind) {
    body = (
      <div className="flex flex-col gap-4">
        <div className="flex items-center gap-2.5">
          {kind.kind === "email" ? <Mail className="size-5 text-muted-foreground" aria-hidden /> : kind.kind === "webhook" ? <Webhook className="size-5 text-muted-foreground" aria-hidden /> : <ProviderMark provider={kind.provider} label={kind.label} className="size-7" />}
          <span className="flex min-w-0 flex-col">
            <b className="text-body font-semibold text-foreground">{kind.label}</b>
            {spec?.blurb && <small className="text-label text-muted-foreground">{spec.blurb}</small>}
          </span>
        </div>
        {kind.kind === "shoutrrr" && spec ? (
          <ProviderForm provider={spec} values={fields} onChange={(key, value) => setFields((p) => ({ ...p, [key]: value }))} onTest={async () => { await sendDraftTest(draft()) }} />
        ) : (
          <>
            <CreateSurfaceField label={webhook ? "URL" : "Email address"} htmlFor={`${ids}-dest`} hint={webhook ? "https only; private addresses are refused. Every POST is signed with an HMAC." : "Where the notification is delivered."}>
              <Input id={`${ids}-dest`} autoFocus value={destination} onChange={(e) => setDestination(e.target.value)} type={webhook ? "url" : "email"} placeholder={webhook ? "https://example.com/hooks/crewship" : "ops@example.com"} className={CREATE_SURFACE_INPUT} />
            </CreateSurfaceField>
            {webhook && (
              <CreateSurfaceField label="Signing secret" htmlFor={`${ids}-secret`} hint="Leave blank and one is made for you, shown once after you save.">
                <Input id={`${ids}-secret`} value={secret} onChange={(e) => setSecret(e.target.value)} type="password" autoComplete="off" placeholder="(made for you)" className={CREATE_SURFACE_INPUT} />
              </CreateSurfaceField>
            )}
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" variant="outline" size="sm" disabled={testing || !whereReady} onClick={() => void testDraft()}>
                {testing ? <Spinner className="size-3" /> : <Send className="size-3.5" aria-hidden />}
                Send test
              </Button>
              {tested && <span role="status" className={cn("text-label", tested.ok ? "text-success" : "text-destructive")}>{tested.message}</span>}
            </div>
          </>
        )}
        <CreateSurfaceField label="Whose" hint={canCreateWorkspace ? "Workspace channels can receive anyone's notifications. Only mine sends just your own." : "Workspace channels need a Manager or Admin; this one is yours alone."}>
          <SettingsSegmented
            label="Whose"
            options={[
              { value: "workspace", label: "Workspace" },
              { value: "mine", label: "Only mine" },
            ]}
            value={personal ? "mine" : "workspace"}
            disabled={!canCreateWorkspace}
            onChange={(v) => {
              setPersonal(v === "mine")
              if (v === "mine") setCategories([])
            }}
          />
        </CreateSurfaceField>
      </div>
    )
  } else {
    body = (
      <div className="flex flex-col gap-4">
        <CreateSurfaceField label="Send only when priority is at least">
          <SettingsSegmented label="Lowest priority" options={PRIORITIES} value={minPriority} onChange={setMinPriority} />
        </CreateSurfaceField>
        {personal ? (
          <p className="text-label text-muted-foreground">A personal channel carries what you choose in Channels › My notifications.</p>
        ) : (
          <CreateSurfaceField label="What goes there" hint="Leave everything off to allow every kind of event.">
            <div className="grid gap-2 sm:grid-cols-2">
              {NOTIFICATION_CATEGORY_GROUPS.map((g) => {
                const all = g.categories.every((c) => categories.includes(c.key))
                return (
                  <section key={g.key} className="rounded-lg border border-border bg-card">
                    <header className="flex items-center gap-2 border-b border-border px-3 py-2">
                      <b className="flex-1 text-body font-semibold text-foreground">{g.label}</b>
                      <Switch
                        size="sm"
                        checked={all}
                        aria-label={`All ${g.label} events`}
                        onCheckedChange={(on) =>
                          setCategories((prev) => (on ? Array.from(new Set([...prev, ...g.categories.map((c) => c.key)])) : prev.filter((k) => !g.categories.some((c) => c.key === k))))
                        }
                      />
                    </header>
                    <ul className="flex flex-col gap-1.5 px-3 py-2">
                      {g.categories.map((c) => {
                        const id = `${ids}-cat-${c.key}`
                        return (
                          <li key={c.key} className="flex items-center gap-2" title={c.hint}>
                            <Checkbox id={id} checked={categories.includes(c.key)} onCheckedChange={() => toggle(c.key)} />
                            <label htmlFor={id} className="cursor-pointer text-label text-foreground">{c.label}</label>
                          </li>
                        )
                      })}
                    </ul>
                  </section>
                )
              })}
            </div>
          </CreateSurfaceField>
        )}
      </div>
    )
  }

  const title = webhook ? "Add webhook" : "Add channel"
  return (
    <CreateSurface open onOpenChange={(open) => !open && onClose()} size="lg" dirty={dirty} discardLabel={webhook ? "this webhook" : "this channel"} onSubmit={() => (revealed ? onClose() : next())}>
      <CreateSurfaceHeader
        concept="integrations"
        context="Integrations"
        title={title}
        description={webhook ? "Crewship sends events to your system as signed JSON." : "Where Crewship reaches people: pick the app, where it posts and what it sends."}
        onClose={onClose}
      />
      {!revealed && <CreateSurfaceSteps ariaLabel="Add channel steps" steps={STEPS} current={step} onJump={(i) => setStep(i)} />}
      <CreateSurfaceBody>
        <div className={cn("grid gap-5", !revealed && "md:grid-cols-[minmax(0,1fr)_minmax(0,260px)]")}>
          <div className="min-w-0">{body}</div>
          {!revealed && <Preview kind={kind} />}
        </div>
      </CreateSurfaceBody>
      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
      {revealed ? (
        <CreateSurfaceFooter onCancel={onClose} guardCancel={false} cancelLabel="Close" primaryLabel="Done" onPrimary={onClose} hint="Copy the secret before you close" />
      ) : (
        <CreateSurfaceFooter
          onCancel={onClose}
          secondary={step > (mode === "webhook" ? 1 : 0) ? <Button variant="ghost" size="sm" onClick={() => setStep(step - 1)}>Back</Button> : undefined}
          hint={step < 2 ? "⌘+Enter to continue · Esc cancel" : "⌘+Enter to connect · Esc cancel"}
          primaryLabel={step < 2 ? "Continue" : webhook ? "Add webhook" : `Connect ${kind?.label ?? ""}`.trim()}
          primaryIcon={step < 2 ? undefined : Plus}
          onPrimary={next}
          primaryDisabled={!ready}
          busy={creating}
        />
      )}
    </CreateSurface>
  )
}

/** How a message looks at the destination — an example, labelled as one. */
function Preview({ kind }: { kind: Kind | null }) {
  const where = kind?.label ?? "the app"
  const content = (
    <div className="flex flex-col gap-2">
      {[
        { tone: "bg-destructive", title: "A routine could not finish", text: "Missing credential · the token was rejected. The crew stopped after step 2 of 5." },
        { tone: "bg-warn", title: "An agent waits for you", text: "Approve publishing the report · due in 3 h" },
      ].map((m) => (
        <div key={m.title} className="flex gap-2.5 rounded-lg border border-border bg-card p-2.5">
          <span className={cn("w-1 shrink-0 rounded-full", m.tone)} aria-hidden />
          <div className="min-w-0">
            <b className="block text-label font-semibold text-foreground">{m.title}</b>
            <p className="text-label text-muted-foreground">{m.text}</p>
            <span className="mt-1 inline-block text-label font-medium text-primary-hover">Open in Crewship</span>
          </div>
        </div>
      ))}
    </div>
  )
  return (
    <aside aria-label="Message preview" className="min-w-0 rounded-card border border-border bg-surface-subtle p-3">
      <details className="md:hidden">
        <summary className="flex cursor-pointer items-center gap-1.5 text-label font-medium text-muted-foreground">
          <Eye className="size-3.5" aria-hidden /> How it looks in {where}
        </summary>
        <div className="mt-2">{content}</div>
      </details>
      <div className="hidden md:block">
        <small className="mb-2 flex items-center gap-1.5 text-label font-medium text-muted-foreground">
          <Eye className="size-3.5" aria-hidden /> How it looks in {where}
        </small>
        {kind?.kind === "webhook" ? (
          <pre className="overflow-x-auto rounded-lg border border-border bg-card p-2.5 font-mono text-label text-foreground">{`{
  "event": "routines.failed",
  "priority": "high",
  "message": "…"
}`}</pre>
        ) : (
          content
        )}
      </div>
    </aside>
  )
}

function SecretReveal({ secret }: { secret: string }) {
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-warn/40 bg-chip-warn-bg px-3 py-3" data-slot="secret-reveal">
      <b className="text-body font-semibold text-chip-warn-fg">Copy the signing secret now — it is shown only once</b>
      <div className="flex items-center gap-2">
        <code className="break-all rounded bg-card px-1.5 py-0.5 font-mono text-label text-foreground">{secret}</code>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Copy signing secret"
          onClick={() => {
            void navigator.clipboard?.writeText(secret)
            toast.success("Secret copied")
          }}
        >
          <Copy className="size-3.5" />
        </Button>
      </div>
      <p className="text-label text-muted-foreground">
        Verify the <code className="font-mono">X-Crewship-Signature</code> HMAC with this secret. It cannot be shown again.
      </p>
    </div>
  )
}
