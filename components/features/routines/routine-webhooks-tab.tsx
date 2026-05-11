"use client"

import { useMemo, useState } from "react"
import { Plus, Trash2, Webhook, Copy, Check, Eye, EyeOff } from "lucide-react"
import { usePipelineWebhooks, type PipelineWebhook } from "@/hooks/use-pipeline-webhooks"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { RoutineListSkeleton } from "./routine-skeletons"
import { Card, EmptyState, Pill, FieldLabel } from "./_shared"

// RoutineWebhooksTab — event-driven trigger CRUD restyled for the
// dashboard. Token + signing secret are revealed once on create
// (Stripe-style); thereafter the UI only shows the public URL and
// last-fired status. Delete + recreate is the rotation path.

interface Props {
  workspaceId: string
  pipelineId: string
  slug: string
}

export function RoutineWebhooksTab({ workspaceId, pipelineId, slug }: Props) {
  const { webhooks, loading, error, create, remove } = usePipelineWebhooks(workspaceId)
  const ours = useMemo(() => webhooks.filter((w) => w.target_pipeline_id === pipelineId), [webhooks, pipelineId])

  const [formOpen, setFormOpen] = useState(false)
  const [name, setName] = useState("")
  const [signingSecret, setSigningSecret] = useState("")
  const [rateLimit, setRateLimit] = useState(60)
  const [busy, setBusy] = useState(false)
  const [justCreated, setJustCreated] = useState<PipelineWebhook | null>(null)

  const submit = async () => {
    setBusy(true)
    try {
      const w = await create({
        name: name || `${slug} webhook`,
        target_pipeline_slug: slug,
        signing_secret: signingSecret || undefined,
        rate_limit_per_min: rateLimit,
        enabled: true,
        inputs_template: {},
      })
      if (w) {
        setJustCreated(w)
        toast.success("Webhook created", {
          description: "Copy the token + signing secret now — they won't be shown again",
        })
      }
      setFormOpen(false)
      setName("")
      setSigningSecret("")
      setRateLimit(60)
    } catch (e) {
      toast.error("Create failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  const del = async (w: PipelineWebhook) => {
    if (!confirm(`Delete webhook "${w.name}"? Existing senders will start failing.`)) return
    try {
      await remove(w.id)
      toast.success("Webhook deleted")
    } catch (e) {
      toast.error("Delete failed", { description: e instanceof Error ? e.message : String(e) })
    }
  }

  if (loading && ours.length === 0) {
    return (
      <Card title="Webhooks" subtitle="loading…">
        <div className="p-4">
          <RoutineListSkeleton rows={2} />
        </div>
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      {error && (
        <Card tone="amber">
          <div className="px-4 py-3 text-sm text-amber-300">{error}</div>
        </Card>
      )}

      {justCreated && <CreatedReveal webhook={justCreated} onDismiss={() => setJustCreated(null)} />}

      {ours.length === 0 && !formOpen ? (
        <Card title="Webhooks">
          <EmptyState
            icon={Webhook}
            title="No webhooks yet"
            description="Create a webhook endpoint that triggers this routine on HTTP POST. Optionally protect it with an HMAC signing secret."
            action={
              <Button
                size="sm"
                variant="default"
                onClick={() => setFormOpen(true)}
                className="h-9 gap-1.5 px-4 text-sm"
              >
                <Plus className="h-3.5 w-3.5" />
                Add webhook
              </Button>
            }
          />
        </Card>
      ) : (
        <Card
          title="Webhooks"
          subtitle={`${ours.length} for this routine`}
          action={
            !formOpen && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => setFormOpen(true)}
                className="h-8 gap-1.5 text-xs"
              >
                <Plus className="h-3 w-3" />
                Add webhook
              </Button>
            )
          }
        >
          <ol className="divide-y divide-white/[0.04]">
            {ours.map((w) => (
              <li key={w.id} className="grid grid-cols-[auto_1fr_auto] items-start gap-3 px-4 py-3">
                <div
                  className={cn(
                    "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg",
                    w.enabled
                      ? "bg-blue-500/20 text-blue-400"
                      : "bg-muted text-muted-foreground",
                  )}
                >
                  <Webhook className="h-4 w-4" />
                </div>
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-sm font-semibold">{w.name}</span>
                    <Pill tone={w.enabled ? "blue" : "default"}>
                      {w.enabled ? "enabled" : "disabled"}
                    </Pill>
                    {w.signing_secret_set && (
                      <Pill tone="emerald">HMAC verified</Pill>
                    )}
                  </div>
                  <div className="font-mono text-[12px] text-muted-foreground">
                    Token <span className="text-foreground/85">{w.token.slice(0, 16)}…</span>
                  </div>
                  <div className="flex flex-wrap items-center gap-x-3 text-[11px] text-muted-foreground">
                    <span>
                      Rate limit <span className="text-foreground/85">{w.rate_limit_per_min}/min</span>
                    </span>
                    <span className="opacity-60">·</span>
                    <span>
                      Fired <span className="text-foreground/85 tabular-nums">{w.fire_count}×</span>
                    </span>
                    {w.last_fired_at && (
                      <>
                        <span className="opacity-60">·</span>
                        <span>
                          Last <span className="text-foreground/85">{new Date(w.last_fired_at).toLocaleString()}</span>
                          {w.last_status && (
                            <span className="ml-1 opacity-70">({w.last_status})</span>
                          )}
                        </span>
                      </>
                    )}
                  </div>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => del(w)}
                  className="h-8 w-8 shrink-0 p-0 text-muted-foreground hover:text-rose-400"
                  title="Delete"
                  aria-label={`Delete webhook ${w.name || w.id}`}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </li>
            ))}
          </ol>
        </Card>
      )}

      {formOpen && (
        <Card title="New webhook">
          <div className="space-y-4 p-4">
            <div>
              <FieldLabel>Name</FieldLabel>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={`${slug} webhook`}
                className="mt-1.5 h-9 text-sm"
              />
            </div>
            <div>
              <FieldLabel>Signing secret (optional)</FieldLabel>
              <Input
                type="password"
                value={signingSecret}
                onChange={(e) => setSigningSecret(e.target.value)}
                placeholder="leave empty to skip HMAC verification"
                className="mt-1.5 h-9 font-mono text-sm"
              />
              <p className="mt-1.5 text-[11px] text-muted-foreground">
                When set, sender must include{" "}
                <span className="font-mono">X-Crewship-Signature: sha256=&lt;hmac&gt;</span> header.
              </p>
            </div>
            <div>
              <FieldLabel>Rate limit per minute</FieldLabel>
              <Input
                type="number"
                value={rateLimit}
                onChange={(e) => setRateLimit(parseInt(e.target.value, 10) || 60)}
                className="mt-1.5 h-9 text-sm"
                min={1}
                max={600}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button size="sm" variant="ghost" onClick={() => setFormOpen(false)} disabled={busy} className="h-9 px-4">
                Cancel
              </Button>
              <Button size="sm" onClick={submit} disabled={busy} className="h-9 px-4">
                {busy ? "Creating…" : "Create webhook"}
              </Button>
            </div>
          </div>
        </Card>
      )}
    </div>
  )
}

function CreatedReveal({ webhook, onDismiss }: { webhook: PipelineWebhook; onDismiss: () => void }) {
  const [copied, setCopied] = useState<string | null>(null)
  const [showSecret, setShowSecret] = useState(false)

  const url = `${typeof window !== "undefined" ? window.location.origin : ""}/api/v1/webhooks/${webhook.token}`

  const copy = (val: string, key: string) => {
    navigator.clipboard.writeText(val).then(() => {
      setCopied(key)
      setTimeout(() => setCopied(null), 1500)
    })
  }

  return (
    <Card tone="emerald">
      <div className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <span className="text-sm font-semibold text-emerald-300">Webhook created — copy values now</span>
            </div>
            <p className="mt-1 text-[12px] text-muted-foreground">
              The signing secret is only shown once. To rotate, delete and recreate.
            </p>
          </div>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 w-7 p-0"
            onClick={onDismiss}
            aria-label="Dismiss webhook reveal panel"
          >
            <span aria-hidden="true" className="text-base">
              ×
            </span>
          </Button>
        </div>
        <RevealField label="Public URL" value={url} copyKey="url" copied={copied} onCopy={copy} />
        <RevealField label="Token" value={webhook.token} copyKey="token" copied={copied} onCopy={copy} mono />
        {webhook.signing_secret && (
          <div>
            <div className="mb-1.5 flex items-center justify-between">
              <FieldLabel className="!normal-case !tracking-normal !text-[11px]">Signing secret</FieldLabel>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setShowSecret((s) => !s)}
                className="h-6 gap-1 px-2 text-[11px]"
                aria-label={showSecret ? "Hide signing secret" : "Show signing secret"}
              >
                {showSecret ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                {showSecret ? "Hide" : "Show"}
              </Button>
            </div>
            <div className="flex items-center gap-1.5">
              <code className="flex-1 truncate rounded-md border border-white/[0.06] bg-black/30 px-2.5 py-1.5 font-mono text-[12px] text-foreground/90">
                {showSecret ? webhook.signing_secret : "•".repeat(40)}
              </code>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => copy(webhook.signing_secret!, "secret")}
                className="h-8 w-8 p-0"
                aria-label="Copy signing secret"
              >
                {copied === "secret" ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
              </Button>
            </div>
          </div>
        )}
      </div>
    </Card>
  )
}

function RevealField({
  label,
  value,
  copyKey,
  copied,
  onCopy,
  mono,
}: {
  label: string
  value: string
  copyKey: string
  copied: string | null
  onCopy: (v: string, k: string) => void
  mono?: boolean
}) {
  return (
    <div>
      <FieldLabel className="!normal-case !tracking-normal !text-[11px]">{label}</FieldLabel>
      <div className="mt-1.5 flex items-center gap-1.5">
        <code
          className={cn(
            "flex-1 truncate rounded-md border border-white/[0.06] bg-black/30 px-2.5 py-1.5 text-[12px] text-foreground/90",
            mono && "font-mono",
          )}
        >
          {value}
        </code>
        <Button
          size="sm"
          variant="ghost"
          onClick={() => onCopy(value, copyKey)}
          className="h-8 w-8 p-0"
          aria-label={`Copy ${label.toLowerCase()}`}
        >
          {copied === copyKey ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </Button>
      </div>
    </div>
  )
}
