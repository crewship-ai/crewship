"use client"

import * as React from "react"
import { Copy, Plus, User } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
  CreateSurfaceSection,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { NOTIFICATION_CATEGORY_GROUPS } from "@/lib/notification-categories"
import { ProviderForm } from "./provider-form"
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
 *
 * The chrome is `CreateSurface` (components/layout/create-surface.tsx), at
 * size `md`. It used to be a bare `sm:max-w-lg` DialogContent with its own
 * `max-h-[85vh] overflow-y-auto` — which put the FOOTER INSIDE THE SCROLLPORT,
 * so on a short viewport "Connect" was below the 18-checkbox category matrix
 * and you had to scroll past everything to find it. That is the defect the
 * shell's fixed footer exists to prevent.
 *
 * This is the step the catalog hands off to, and until now only the catalog
 * had been migrated: picking Slack in a unified surface opened an old-style
 * one, which is worse than not having migrated either. Notes on the parts that
 * are not a straight swap:
 *
 *  · SEND TEST STAYS IN THE BODY. It belongs to the provider's fields (it
 *    tests exactly those, unsaved) and it is not this surface's primary —
 *    `Connect` is. Promoting it to the footer's `secondary` would put a
 *    non-committing action next to the committing one.
 *  · EVERY CHECKBOX IS NAMED BY ITS OWN `<label htmlFor>` — the eighteen
 *    category cells and the personal-connection row alike.
 *    `CreateSurfaceToggleRow` renders its label as plain text in a `<div>`, so
 *    a bare Checkbox in its `control` slot has no accessible name at all, and
 *    the words beside it are not a tap target either.
 *  · THE FAILURE PATH keeps its toast AND gains the refusal band. The toast is
 *    the notification the rest of the app uses; the band is the one that does
 *    not fade while you are still reading the form it refused.
 *  · THE ONE-TIME SECRET REVEAL replaces the body and hides the footer, as it
 *    did before: at that point there is nothing left to submit, and `Done` is
 *    the only way forward.
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
  // What the server said when it said no. Shown in the shell's band, which
  // sits outside the scrollport; the toast below it is kept because it is the
  // notification the rest of the app uses, but the toast is what fades.
  const [refusal, setRefusal] = React.useState<string | null>(null)

  const ids = React.useId()
  const destinationId = `${ids}-destination`
  const secretId = `${ids}-secret`
  const personalId = `${ids}-personal`
  const priorityId = `${ids}-priority`

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
    setRefusal(null)
  }, [target, canCreateWorkspace])

  const spec = target?.provider ? providers.find((p) => p.provider === target.provider) : undefined

  const ready = target
    ? target.kind === "shoutrrr"
      ? spec !== undefined &&
        spec.fields.every((f) => !f.required || (fields[f.key] ?? "").trim() !== "")
      : destination.trim() !== ""
    : false

  // Only what the person typed or ticked counts. The reveal is NOT dirty: the
  // channel is already saved by then, so prompting to "discard" it on Esc
  // would name something that no longer exists.
  const dirty =
    revealed === null &&
    (Object.values(fields).some((v) => v.trim() !== "") ||
      destination.trim() !== "" ||
      secret.trim() !== "" ||
      categories.length > 0 ||
      minPriority !== "low" ||
      personal !== !canCreateWorkspace)

  const submit = async () => {
    if (!target || !ready || creating) return
    setCreating(true)
    setRefusal(null)
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
      const message = e instanceof Error ? e.message : "Failed to add the connection"
      setRefusal(message)
      toast.error(message)
    } finally {
      setCreating(false)
    }
  }

  if (!target) return null

  return (
    <CreateSurface
      open
      onOpenChange={(open) => !open && onClose()}
      size="md"
      dirty={dirty}
      discardLabel="this connection"
      // ⌘↵ comes from the shell. `submit` keeps its own readiness guard because
      // the keyboard route reaches it while the footer's primary is disabled.
      onSubmit={() => void submit()}
    >
      <CreateSurfaceHeader
        concept="integrations"
        context="Integrations"
        title={`Connect ${target.label}`}
        description={
          spec?.blurb ??
          (target.kind === "webhook"
            ? "Crewship POSTs a signed payload to an endpoint you control."
            : "Crewship sends to this address using the instance's mail transport.")
        }
        onClose={onClose}
      />

      <CreateSurfaceBody className="flex flex-col gap-4">
        {revealed ? (
          <SecretReveal secret={revealed} onDone={onClose} />
        ) : (
          <>
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
              <CreateSurfaceField
                label={target.kind === "webhook" ? "Endpoint URL" : "Email address"}
                htmlFor={destinationId}
                hint={
                  target.kind === "webhook"
                    ? "Must be reachable from this instance. We sign every POST with an HMAC."
                    : "Where the notification is delivered."
                }
              >
                <Input
                  id={destinationId}
                  value={destination}
                  onChange={(e) => setDestination(e.target.value)}
                  type={target.kind === "email" ? "email" : "url"}
                  placeholder={
                    target.kind === "webhook"
                      ? "https://example.com/hooks/crewship"
                      : "ops@example.com"
                  }
                  className={CREATE_SURFACE_INPUT}
                />
              </CreateSurfaceField>
            )}

            {target.kind === "webhook" && (
              <CreateSurfaceField
                label="Signing secret"
                htmlFor={secretId}
                hint="Leave blank and we generate one, shown once after you save."
              >
                <Input
                  id={secretId}
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                  type="password"
                  autoComplete="off"
                  placeholder="(auto-generate)"
                  className={CREATE_SURFACE_INPUT}
                />
              </CreateSurfaceField>
            )}

            {/* The label is a real <label htmlFor> inside the row's label
                slot, which is the same workaround skills/import-dialog.tsx
                landed on: CreateSurfaceToggleRow renders its label as plain
                text in a <div>, so a bare Checkbox in `control` has no
                accessible name and the words are not a tap target. */}
            <CreateSurfaceToggleRow
              icon={User}
              accent="teal"
              label={
                <label htmlFor={personalId} className="cursor-pointer">
                  Personal connection
                </label>
              }
              hint={
                canCreateWorkspace
                  ? "Only you can route to it, and only you see it. Leave unchecked to share it with the workspace."
                  : "Workspace-wide connections need the ADMIN or OWNER role, so this one is yours alone."
              }
              control={
                <Checkbox
                  id={personalId}
                  checked={personal}
                  disabled={!canCreateWorkspace}
                  onCheckedChange={(c) => {
                    const next = c === true
                    setPersonal(next)
                    // An admin allowlist is meaningless on a channel only its
                    // owner can route to.
                    if (next) setCategories([])
                  }}
                />
              }
            />

            {!personal && (
              <>
                <CreateSurfaceField
                  label="Categories"
                  hint="Which categories anyone may route here. Leave empty to allow every category."
                >
                  {/* A nested scrollport, as before: eighteen cells unrolled
                      into the body push the rest of the form off the surface,
                      and the footer no longer moves with it. */}
                  <div className="flex max-h-44 flex-col gap-3 overflow-y-auto overscroll-contain rounded-md border border-hairline p-2">
                    {NOTIFICATION_CATEGORY_GROUPS.map((group) => (
                      <CreateSurfaceSection key={group.key} title={group.label} className="gap-1.5">
                        <div className="flex flex-wrap gap-x-3 gap-y-2">
                          {group.categories.map((cat) => {
                            const id = `${ids}-cat-${cat.key}`
                            return (
                              <div key={cat.key} className="flex items-center gap-1.5" title={cat.hint}>
                                <Checkbox
                                  id={id}
                                  checked={categories.includes(cat.key)}
                                  onCheckedChange={() =>
                                    setCategories((prev) =>
                                      prev.includes(cat.key)
                                        ? prev.filter((c) => c !== cat.key)
                                        : [...prev, cat.key],
                                    )
                                  }
                                />
                                <label
                                  htmlFor={id}
                                  className="cursor-pointer text-[11px] text-muted-foreground"
                                >
                                  {cat.label}
                                </label>
                              </div>
                            )
                          })}
                        </div>
                      </CreateSurfaceSection>
                    ))}
                  </div>
                </CreateSurfaceField>

                <CreateSurfaceField
                  label="Priority floor"
                  htmlFor={priorityId}
                  hint="Skip anything below this priority."
                >
                  <Select
                    value={minPriority}
                    onValueChange={(v) => setMinPriority(v as typeof minPriority)}
                  >
                    {/* aria-label as well as the field's <label htmlFor>: the
                        trigger is a <button>, and HTML-AAM does not name a
                        button from an associated label the way it does an
                        <input>. Without this the combobox is anonymous. */}
                    <SelectTrigger
                      id={priorityId}
                      aria-label="Priority floor"
                      className={cn(CREATE_SURFACE_INPUT, "w-[180px]")}
                    >
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
                </CreateSurfaceField>
              </>
            )}
          </>
        )}
      </CreateSurfaceBody>

      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      {!revealed && (
        <CreateSurfaceFooter
          onCancel={onClose}
          guardCancel
          primaryLabel="Connect"
          primaryIcon={Plus}
          onPrimary={() => void submit()}
          primaryDisabled={!ready}
          busy={creating}
        />
      )}
    </CreateSurface>
  )
}

function SecretReveal({ secret, onDone }: { secret: string; onDone: () => void }) {
  return (
    <div className="space-y-2 rounded-lg border border-warn/40 bg-warn/[0.05] px-3 py-3">
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
      <Button
        variant="soft"
        size="sm"
        className="h-8 text-xs max-sm:h-12 max-sm:text-sm"
        onClick={onDone}
      >
        Done
      </Button>
    </div>
  )
}
