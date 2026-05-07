"use client"

import { useMemo, useState } from "react"
import { Plus, Trash2, Webhook, Copy, Check, Eye, EyeOff } from "lucide-react"
import { usePipelineWebhooks, type PipelineWebhook } from "@/hooks/use-pipeline-webhooks"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { RoutineListSkeleton } from "./routine-skeletons"

// RoutineWebhooksTab — event-driven trigger CRUD. Token + signing
// secret are revealed once on create (Stripe-style); thereafter the
// UI only shows the public URL and last-fired status. Delete + recreate
// is the rotation path — design choice from the backend.

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

  if (loading && ours.length === 0) return <RoutineListSkeleton rows={2} />

  return (
    <div className="space-y-3">
      {error && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-400">
          {error}
        </div>
      )}

      {/* Just-created reveal card */}
      {justCreated && (
        <CreatedReveal webhook={justCreated} onDismiss={() => setJustCreated(null)} />
      )}

      {/* List */}
      {ours.length === 0 && !formOpen ? (
        <div className="rounded-md border border-dashed border-border/60 p-6 text-center">
          <Webhook className="mx-auto mb-2 h-6 w-6 text-muted-foreground/50" />
          <p className="text-xs text-muted-foreground">No webhooks yet for this routine.</p>
          <Button size="sm" variant="outline" onClick={() => setFormOpen(true)} className="mt-2 h-7 gap-1.5 text-xs">
            <Plus className="h-3 w-3" />
            Add webhook
          </Button>
        </div>
      ) : (
        <ol className="space-y-1.5">
          {ours.map((w) => (
            <li key={w.id} className="rounded-md border border-white/[0.06] bg-card/40 p-2.5 text-[11px]">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium">{w.name}</span>
                    <Badge variant="outline" className={cn("text-[9px]", !w.enabled && "opacity-50")}>
                      {w.enabled ? "enabled" : "disabled"}
                    </Badge>
                    {w.signing_secret_set && (
                      <Badge variant="outline" className="text-[9px] border-emerald-500/30 text-emerald-400">
                        HMAC
                      </Badge>
                    )}
                  </div>
                  <div className="mt-0.5 font-mono text-[10px] text-muted-foreground">
                    Token: {w.token.slice(0, 12)}…
                  </div>
                  <div className="mt-0.5 text-[10px] text-muted-foreground">
                    Rate limit: {w.rate_limit_per_min}/min · Fired {w.fire_count}×
                  </div>
                  {w.last_fired_at && (
                    <div className="text-[10px] text-muted-foreground">
                      Last: {new Date(w.last_fired_at).toLocaleString()} ({w.last_status ?? "?"})
                    </div>
                  )}
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => del(w)}
                  className="h-6 w-6 p-0 text-muted-foreground hover:text-red-400"
                  title="Delete"
                >
                  <Trash2 className="h-3 w-3" />
                </Button>
              </div>
            </li>
          ))}
          {!formOpen && (
            <li>
              <Button size="sm" variant="outline" onClick={() => setFormOpen(true)} className="w-full h-7 gap-1.5 text-xs">
                <Plus className="h-3 w-3" />
                Add another webhook
              </Button>
            </li>
          )}
        </ol>
      )}

      {/* Inline form */}
      {formOpen && (
        <div className="space-y-2 rounded-md border border-white/[0.1] bg-card/60 p-3">
          <h4 className="text-xs font-medium">New webhook</h4>
          <div>
            <label className="text-[10px] uppercase tracking-wider text-muted-foreground">Name</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={`${slug} webhook`} className="h-7 text-xs" />
          </div>
          <div>
            <label className="text-[10px] uppercase tracking-wider text-muted-foreground">
              Signing secret (optional)
            </label>
            <Input
              type="password"
              value={signingSecret}
              onChange={(e) => setSigningSecret(e.target.value)}
              placeholder="leave empty to skip HMAC verification"
              className="h-7 font-mono text-xs"
            />
            <p className="mt-1 text-[10px] text-muted-foreground">
              When set, sender must include X-Crewship-Signature: sha256=&lt;hmac&gt; header.
            </p>
          </div>
          <div>
            <label className="text-[10px] uppercase tracking-wider text-muted-foreground">
              Rate limit per minute
            </label>
            <Input
              type="number"
              value={rateLimit}
              onChange={(e) => setRateLimit(parseInt(e.target.value, 10) || 60)}
              className="h-7 text-xs"
              min={1}
              max={600}
            />
          </div>
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setFormOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button size="sm" onClick={submit} disabled={busy}>
              {busy ? "Creating…" : "Create"}
            </Button>
          </div>
        </div>
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
    <div className="rounded-md border border-emerald-500/30 bg-emerald-500/5 p-3 text-xs">
      <div className="flex items-start justify-between">
        <div>
          <h4 className="font-medium text-emerald-300">Webhook created — copy values now</h4>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            The signing secret is only shown once. To rotate, delete and recreate.
          </p>
        </div>
        <Button size="sm" variant="ghost" className="h-6 w-6 p-0" onClick={onDismiss}>
          ×
        </Button>
      </div>
      <div className="mt-3 space-y-2">
        <RevealField label="Public URL" value={url} copyKey="url" copied={copied} onCopy={copy} />
        <RevealField label="Token" value={webhook.token} copyKey="token" copied={copied} onCopy={copy} mono />
        {webhook.signing_secret && (
          <div>
            <div className="mb-1 flex items-center justify-between">
              <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
                Signing secret
              </span>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setShowSecret((s) => !s)}
                className="h-5 px-1.5 text-[10px]"
              >
                {showSecret ? <EyeOff className="h-2.5 w-2.5" /> : <Eye className="h-2.5 w-2.5" />}
                {showSecret ? "Hide" : "Show"}
              </Button>
            </div>
            <div className="flex items-center gap-1">
              <code className="flex-1 rounded bg-background px-2 py-1 font-mono text-[11px]">
                {showSecret ? webhook.signing_secret : "•".repeat(40)}
              </code>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => copy(webhook.signing_secret!, "secret")}
                className="h-7 w-7 p-0"
              >
                {copied === "secret" ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
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
      <span className="text-[10px] uppercase tracking-wider text-muted-foreground">{label}</span>
      <div className="mt-0.5 flex items-center gap-1">
        <code className={cn("flex-1 truncate rounded bg-background px-2 py-1 text-[11px]", mono && "font-mono")}>
          {value}
        </code>
        <Button size="sm" variant="ghost" onClick={() => onCopy(value, copyKey)} className="h-7 w-7 p-0">
          {copied === copyKey ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
        </Button>
      </div>
    </div>
  )
}
