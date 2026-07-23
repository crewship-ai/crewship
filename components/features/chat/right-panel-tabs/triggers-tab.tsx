"use client"

import { useState } from "react"
import {
  Clock,
  Globe,
  Copy,
  CheckCircle2,
  XCircle,
  KeyRound,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { useAgentFetch } from "@/hooks/use-agent-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"

interface AgentScheduleInfo {
  schedule_cron: string | null
  schedule_prompt: string | null
  schedule_enabled: boolean
  schedule_last_run: string | null
  schedule_next_run: string | null
  // Presence flag only — the secret value is show-once (#999) and never
  // returned by any read endpoint. Rotate via
  // `crewship agent rotate-webhook-secret <slug>`.
  webhook_secret_set?: boolean
  crew_id: string | null
  slug: string
}

export interface TriggersTabProps {
  agentId: string
  workspaceId: string | null
}

export function TriggersTab({ agentId, workspaceId }: TriggersTabProps) {
  const [copied, setCopied] = useState(false)
  // Webhook signing-secret rotation (#999 / #1378). The secret is show-once:
  // no read endpoint returns it, so a fresh POST is the only way to obtain one.
  // We hold the plaintext in local state ONLY until the user dismisses it.
  const [rotating, setRotating] = useState(false)
  const [newSecret, setNewSecret] = useState<string | null>(null)
  const [secretSet, setSecretSet] = useState<boolean | null>(null)
  const [secretCopied, setSecretCopied] = useState(false)

  // Rotation is the same per-agent edit gate as the settings PATCH
  // (canEditAgent server-side: OWNER/ADMIN always, MANAGER conditionally).
  // MEMBER/VIEWER can't update agents, so hide the control for them.
  const { abilities } = useAbilities()
  const canRotate = abilities.can("update", "Agent")

  const { data: agent, loading } = useAgentFetch<AgentScheduleInfo>(
    async (signal) => {
      const r = await apiFetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, { signal })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      return r.json()
    },
    [agentId, workspaceId],
    { enabled: workspaceId !== null, logLabel: "TriggersTab" },
  )

  if (loading) return <div className="flex items-center justify-center h-full"><Spinner className="h-5 w-5 text-muted-foreground" /></div>
  if (!workspaceId) return <div className="p-4 text-label text-muted-foreground">Select a workspace to view triggers.</div>
  if (!agent) return <div className="p-4 text-label text-muted-foreground">Unable to load agent</div>

  const webhookUrl = agent.crew_id && agent.slug
    ? `/api/v1/webhooks/${agent.crew_id}/${agentId}/trigger`
    : null

  // Local state wins once we've rotated in this session; otherwise fall back to
  // the presence flag from GET.
  const hasSecret = secretSet ?? agent.webhook_secret_set ?? false

  async function handleRotate() {
    if (rotating) return
    setRotating(true)
    setNewSecret(null)
    try {
      const r = await apiFetch(
        `/api/v1/agents/${agentId}/webhook-secret/rotate?workspace_id=${workspaceId}`,
        { method: "POST" },
      )
      if (!r.ok) {
        let msg = `HTTP ${r.status}`
        try {
          const e = (await r.json()) as { error?: string; detail?: string }
          msg = e.error ?? e.detail ?? msg
        } catch {
          /* keep the status fallback */
        }
        toast.error(`Failed to rotate secret: ${msg}`)
        return
      }
      const body = (await r.json()) as { webhook_secret?: string }
      if (body.webhook_secret) {
        // Show-once: the previous secret stops validating immediately, so make
        // it clear the operator must copy it into the external system now.
        setNewSecret(body.webhook_secret)
        setSecretSet(true)
        setSecretCopied(false)
        toast.success("New webhook secret generated — copy it now, it won't be shown again")
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to rotate secret")
    } finally {
      setRotating(false)
    }
  }

  return (
    <div className="p-3 space-y-4 text-sm">
      {/* Cron Schedule */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Clock className="h-3 w-3" />
          Schedule
        </div>
        {agent.schedule_cron ? (
          <div className="space-y-1.5">
            <div className="flex items-center gap-2">
              <code className="text-label bg-accent px-1.5 py-0.5 rounded font-mono">{agent.schedule_cron}</code>
              {agent.schedule_enabled ? (
                <span className="flex items-center gap-1 text-micro text-emerald-500"><CheckCircle2 className="h-3 w-3" /> Active</span>
              ) : (
                <span className="flex items-center gap-1 text-micro text-muted-foreground"><XCircle className="h-3 w-3" /> Disabled</span>
              )}
            </div>
            {agent.schedule_prompt && (
              <p className="text-label text-muted-foreground line-clamp-2">{agent.schedule_prompt}</p>
            )}
            {agent.schedule_next_run && (
              <p className="text-micro text-muted-foreground">
                Next run: {new Date(agent.schedule_next_run).toLocaleString()}
              </p>
            )}
            {agent.schedule_last_run && (
              <p className="text-micro text-muted-foreground">
                Last run: {new Date(agent.schedule_last_run).toLocaleString()}
              </p>
            )}
          </div>
        ) : (
          <p className="text-label text-muted-foreground">No schedule configured. Set one in Agent Settings &rarr; Schedule.</p>
        )}
      </div>

      {/* Webhook */}
      <div className="space-y-2">
        <div className="flex items-center gap-1.5 text-label font-medium text-muted-foreground uppercase tracking-wider">
          <Globe className="h-3 w-3" />
          Webhook
        </div>
        {webhookUrl ? (
          <div className="space-y-1.5">
            <div className="flex items-center gap-1">
              <code className="text-micro bg-accent px-1.5 py-0.5 rounded font-mono truncate flex-1">{webhookUrl}</code>
              <button
                type="button"
                aria-label={copied ? "Webhook URL copied" : "Copy webhook URL"}
                onClick={async () => {
                  // Clipboard write can reject if the page loses focus or
                  // the permission is denied — surface a toast instead of
                  // leaving an unhandled promise rejection.
                  try {
                    await navigator.clipboard.writeText(window.location.origin + webhookUrl)
                    setCopied(true)
                    setTimeout(() => setCopied(false), 2000)
                  } catch {
                    toast.error("Failed to copy webhook URL")
                  }
                }}
                className="p-1 rounded hover:bg-accent text-muted-foreground"
              >
                {copied ? <CheckCircle2 className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
              </button>
            </div>
            <p className="text-micro text-muted-foreground">
              POST with JSON body.{" "}
              {hasSecret
                ? "Signature header required (X-Signature: hex HMAC-SHA256 of the raw body)."
                : "No signing secret configured — deliveries are unsigned."}
            </p>

            {/* Signing secret rotation (#1378). Show-once: the plaintext is
                displayed exactly once, right after minting. */}
            {canRotate && (
              <div className="pt-1 space-y-1.5">
                <div className="flex items-center gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 px-2.5 text-xs gap-1.5"
                    disabled={rotating}
                    onClick={() => { void handleRotate() }}
                    data-testid="webhook-rotate-secret"
                  >
                    {rotating ? <Spinner className="h-3 w-3" /> : <KeyRound className="h-3 w-3" />}
                    {rotating ? "Rotating…" : hasSecret ? "Rotate secret" : "Generate secret"}
                  </Button>
                  {hasSecret && !newSecret && (
                    <span className="flex items-center gap-1 text-micro text-emerald-500">
                      <CheckCircle2 className="h-3 w-3" /> Secret set
                    </span>
                  )}
                </div>

                {newSecret && (
                  <div
                    className="rounded-md border border-amber-500/40 bg-amber-500/5 px-2.5 py-2 space-y-1.5"
                    data-testid="webhook-secret-reveal"
                  >
                    <p className="text-micro text-amber-600 dark:text-amber-400">
                      Copy this now — it is shown once and the previous secret already stopped validating.
                    </p>
                    <div className="flex items-center gap-1">
                      <code className="text-micro bg-accent px-1.5 py-0.5 rounded font-mono truncate flex-1">
                        {newSecret}
                      </code>
                      <button
                        type="button"
                        aria-label={secretCopied ? "Secret copied" : "Copy webhook secret"}
                        onClick={async () => {
                          try {
                            await navigator.clipboard.writeText(newSecret)
                            setSecretCopied(true)
                            setTimeout(() => setSecretCopied(false), 2000)
                          } catch {
                            toast.error("Failed to copy secret")
                          }
                        }}
                        className="p-1 rounded hover:bg-accent text-muted-foreground"
                      >
                        {secretCopied ? <CheckCircle2 className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
                      </button>
                    </div>
                    <button
                      type="button"
                      onClick={() => setNewSecret(null)}
                      className="text-micro text-muted-foreground hover:text-foreground underline"
                    >
                      I&apos;ve stored it — dismiss
                    </button>
                  </div>
                )}
              </div>
            )}
          </div>
        ) : (
          <p className="text-label text-muted-foreground">Assign agent to a crew to enable webhooks.</p>
        )}
      </div>
    </div>
  )
}
