"use client"

import * as React from "react"
import { Copy, Plus, User } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { NOTIFICATION_CATEGORY_GROUPS } from "@/lib/notification-categories"
import { ProviderForm } from "@/components/features/settings/sections/provider-form"
import type {
  ChannelCreateBody,
  ChannelDraftTestBody,
  CreatedChannel,
  NotificationProvider,
} from "@/hooks/use-notification-channels"

/**
 * Add a connection — the form, reached by picking a service in the catalog.
 *
 * On the previous page this lived in an always-open card at the top, with a
 * <Select> of every provider inside it. That put the least-used control (a
 * blank creation form) above the most-used one (the list of what you already
 * have), and it meant the only way to see the available services was to open
 * a dropdown mid-form. Here the catalog answers "what can I connect", and this
 * dialog answers "connect this one" — with the service already chosen, so the
 * first question is never "which of these 11 do I want".
 */

export type ChannelKindChoice = "shoutrrr" | "email" | "webhook"

export interface AddChannelTarget {
  /** "shoutrrr" plus a provider, or one of the built-in transports. */
  kind: ChannelKindChoice
  provider?: string
  label: string
}

interface AddChannelDialogProps {
  target: AddChannelTarget | null
  onClose: () => void
  providers: NotificationProvider[]
  /** false = this visitor may only create PERSONAL channels. */
  canCreateWorkspace: boolean
  create: (body: ChannelCreateBody) => Promise<CreatedChannel | null>
  sendDraftTest: (body: ChannelDraftTestBody) => Promise<unknown>
}

export function AddChannelDialog({
  target,
  onClose,
  providers,
  canCreateWorkspace,
  create,
  sendDraftTest,
}: AddChannelDialogProps) {
  const [fields, setFields] = React.useState<Record<string, string>>({})
  const [destination, setDestination] = React.useState("")
  const [secret, setSecret] = React.useState("")
  // Default to a personal channel when that is the only thing this visitor may
  // create — offering a workspace channel they cannot save is a form that
  // fails on submit for a reason it knew about the whole time.
  const [personal, setPersonal] = React.useState(!canCreateWorkspace)
  const [categories, setCategories] = React.useState<string[]>([])
  const [minPriority, setMinPriority] = React.useState<"low" | "medium" | "high" | "urgent">("low")
  const [creating, setCreating] = React.useState(false)
  const [revealed, setRevealed] = React.useState<string | null>(null)

  // Reset every time a different service is picked: field keys differ per
  // provider, and carrying a stale webhook_url into Telegram would submit a
  // value the user never typed for it.
  React.useEffect(() => {
    if (!target) return
    setFields({})
    setDestination("")
    setSecret("")
    setCategories([])
    setMinPriority("low")
    setPersonal(!canCreateWorkspace)
    setRevealed(null)
  }, [target, canCreateWorkspace])

  const spec = target?.provider ? providers.find((p) => p.provider === target.provider) : undefined

  const ready = target
    ? target.kind === "shoutrrr"
      ? spec !== undefined &&
        spec.fields.every((f) => !f.required || (fields[f.key] ?? "").trim() !== "")
      : destination.trim() !== ""
    : false

  const submit = async () => {
    if (!target || !ready) return
    setCreating(true)
    try {
      const body: ChannelCreateBody =
        target.kind === "shoutrrr"
          ? { type: "shoutrrr", provider: target.provider, fields }
          : target.kind === "webhook"
            ? {
                type: "webhook",
                url: destination.trim(),
                ...(secret.trim() ? { secret: secret.trim() } : {}),
              }
            : { type: "email", to: destination.trim() }

      const created = await create({
        ...body,
        personal,
        ...(categories.length > 0 ? { categories } : {}),
        ...(minPriority !== "low" ? { min_priority: minPriority } : {}),
      })
      toast.success(`${target.label} connected`)
      // Only the webhook signing secret is worth revealing — the caller needs
      // it to verify our HMAC. A composed chat URL is built from values the
      // user already holds, so echoing it just puts a token on screen.
      if (created?.secret && target.kind === "webhook") {
        setRevealed(created.secret)
      } else {
        onClose()
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to add the connection")
    } finally {
      setCreating(false)
    }
  }

  if (!target) return null

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="text-sm">Connect {target.label}</DialogTitle>
          <DialogDescription className="text-xs">
            {spec?.blurb ??
              (target.kind === "webhook"
                ? "Crewship POSTs a signed payload to an endpoint you control."
                : "Crewship sends to this address using the instance's mail transport.")}
          </DialogDescription>
        </DialogHeader>

        {revealed ? (
          <SecretReveal secret={revealed} onDone={onClose} />
        ) : (
          <div className="space-y-4">
            {target.kind === "shoutrrr" && spec && (
              <ProviderForm
                provider={spec}
                values={fields}
                onChange={(key, value) => setFields((p) => ({ ...p, [key]: value }))}
                onTest={async () => {
                  await sendDraftTest({ type: "shoutrrr", provider: target.provider, fields })
                }}
              />
            )}

            {target.kind !== "shoutrrr" && (
              <Field
                label={target.kind === "webhook" ? "Endpoint URL" : "Email address"}
                help={
                  target.kind === "webhook"
                    ? "Must be reachable from this instance. We sign every POST with an HMAC."
                    : "Where the notification is delivered."
                }
              >
                <Input
                  value={destination}
                  onChange={(e) => setDestination(e.target.value)}
                  type={target.kind === "email" ? "email" : "url"}
                  placeholder={
                    target.kind === "webhook"
                      ? "https://example.com/hooks/crewship"
                      : "ops@example.com"
                  }
                  className="h-8 text-xs"
                />
              </Field>
            )}

            {target.kind === "webhook" && (
              <Field
                label="Signing secret"
                help="Leave blank and we generate one, shown once after you save."
              >
                <Input
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                  type="password"
                  autoComplete="off"
                  placeholder="(auto-generate)"
                  className="h-8 text-xs"
                />
              </Field>
            )}

            <label className="flex cursor-pointer items-start gap-2">
              <Checkbox
                checked={personal}
                disabled={!canCreateWorkspace}
                onCheckedChange={(c) => {
                  const next = c === true
                  setPersonal(next)
                  // An admin allowlist is meaningless on a channel only its
                  // owner can route to.
                  if (next) setCategories([])
                }}
                className="mt-0.5"
              />
              <span className="text-[11px] leading-relaxed text-muted-foreground">
                <span className="flex items-center gap-1 font-medium text-foreground/85">
                  <User className="size-3" /> Personal connection
                </span>
                {canCreateWorkspace
                  ? "Only you can route to it, and only you see it. Leave unchecked to share it with the workspace."
                  : "Workspace-wide connections need the ADMIN or OWNER role, so this one is yours alone."}
              </span>
            </label>

            {!personal && (
              <>
                <Field
                  label="Categories"
                  help="Which categories anyone may route here. Leave empty to allow every category."
                >
                  <div className="flex max-h-44 flex-col gap-2 overflow-y-auto rounded-md border border-white/[0.07] p-2">
                    {NOTIFICATION_CATEGORY_GROUPS.map((group) => (
                      <div key={group.key} className="flex flex-col gap-1">
                        <span className="text-[10px] font-semibold uppercase tracking-wider text-foreground/45">
                          {group.label}
                        </span>
                        <div className="flex flex-wrap gap-x-3 gap-y-1.5">
                          {group.categories.map((cat) => (
                            <label
                              key={cat.key}
                              className="flex cursor-pointer items-center gap-1.5"
                              title={cat.hint}
                            >
                              <Checkbox
                                checked={categories.includes(cat.key)}
                                onCheckedChange={() =>
                                  setCategories((prev) =>
                                    prev.includes(cat.key)
                                      ? prev.filter((c) => c !== cat.key)
                                      : [...prev, cat.key],
                                  )
                                }
                              />
                              <span className="text-[11px] text-muted-foreground">{cat.label}</span>
                            </label>
                          ))}
                        </div>
                      </div>
                    ))}
                  </div>
                </Field>

                <Field label="Priority floor" help="Skip anything below this priority.">
                  <Select
                    value={minPriority}
                    onValueChange={(v) => setMinPriority(v as typeof minPriority)}
                  >
                    <SelectTrigger className="h-8 w-[180px] text-xs">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="low" className="text-xs">
                        Low — no floor
                      </SelectItem>
                      <SelectItem value="medium" className="text-xs">
                        Medium
                      </SelectItem>
                      <SelectItem value="high" className="text-xs">
                        High
                      </SelectItem>
                      <SelectItem value="urgent" className="text-xs">
                        Urgent
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </>
            )}
          </div>
        )}

        {!revealed && (
          <DialogFooter>
            <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={onClose}>
              Cancel
            </Button>
            <Button
              variant="soft"
              size="sm"
              className="h-7 gap-1.5 text-xs"
              disabled={!ready || creating}
              onClick={submit}
            >
              {creating ? <Spinner className="size-3" /> : <Plus className="h-3 w-3" />}
              Connect
            </Button>
          </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Field({
  label,
  help,
  children,
}: {
  label: string
  help?: string
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <div className="text-[11px] font-medium text-foreground/85">{label}</div>
      {children}
      {help && <p className="text-[11px] leading-relaxed text-muted-foreground">{help}</p>}
    </div>
  )
}

function SecretReveal({ secret, onDone }: { secret: string; onDone: () => void }) {
  return (
    <div className="space-y-2 rounded-lg border border-amber-500/40 bg-amber-500/[0.05] px-3 py-3">
      <div className="text-xs font-medium text-foreground/90">
        Signing secret — shown only once
      </div>
      <div className="flex items-center gap-2">
        <code className="break-all rounded bg-black/30 px-1.5 py-0.5 font-mono text-[11px]">
          {secret}
        </code>
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6 shrink-0"
          aria-label="Copy signing secret"
          onClick={() => {
            navigator.clipboard?.writeText(secret)
            toast.success("Secret copied")
          }}
        >
          <Copy className="size-3" />
        </Button>
      </div>
      <p className="text-[11px] leading-relaxed text-muted-foreground">
        Verify the <code className="font-mono">X-Crewship-Signature</code> HMAC with this secret.
        We cannot show it again.
      </p>
      <Button variant="soft" size="sm" className="h-7 text-xs" onClick={onDone}>
        Done
      </Button>
    </div>
  )
}
