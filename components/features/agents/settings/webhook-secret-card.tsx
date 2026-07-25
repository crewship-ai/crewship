"use client"

import { useCallback, useState } from "react"
import { Copy, KeyRound, RefreshCw } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"

// #1378 item 3 — webhook signing-secret rotation was CLI-only
// (cmd_agent_webhook.go). The endpoint has existed since #999; this is the
// missing UI control, not new backend.
//
// Show-once semantics are the whole design: POST .../webhook-secret/rotate is
// the ONLY thing that ever returns a plaintext secret, and the previous one
// stops validating the instant it returns. So the component has to make the
// "copy it now, you cannot get it back" contract impossible to miss.

export function WebhookSecretCard({ agentId }: { agentId: string }) {
  const { role } = useAbilities()
  // Mirrors canEditAgent server-side: OWNER/ADMIN always, MANAGER conditionally
  // (per-agent, which the client can't evaluate) — so offer it to MANAGER too
  // and let the 403 speak if the agent isn't theirs.
  const canRotate = role === "OWNER" || role === "ADMIN" || role === "MANAGER"

  const [rotating, setRotating] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)

  const rotate = useCallback(async () => {
    if (
      !window.confirm(
        "Rotate the webhook signing secret?\n\n" +
          "The current secret stops validating immediately — any webhook sender " +
          "still using it will start failing until you update it. The new secret " +
          "is shown once and cannot be retrieved again.",
      )
    ) {
      return
    }
    setRotating(true)
    try {
      const res = await apiFetch(`/api/v1/agents/${agentId}/webhook-secret/rotate`, {
        method: "POST",
      })
      const body = (await res.json().catch(() => ({}))) as {
        webhook_secret?: string
        error?: string
      }
      if (!res.ok || !body.webhook_secret) {
        toast.error(body.error || `Rotation failed (HTTP ${res.status})`)
        return
      }
      setSecret(body.webhook_secret)
      toast.success("Webhook secret rotated — copy it now, it is shown once")
    } catch {
      toast.error("Network error while rotating the webhook secret")
    } finally {
      setRotating(false)
    }
  }, [agentId])

  const copy = useCallback(async () => {
    if (!secret) return
    try {
      await navigator.clipboard.writeText(secret)
      toast.success("Copied to clipboard")
    } catch {
      toast.error("Could not copy — select the value and copy it manually")
    }
  }, [secret])

  return (
    <div className="space-y-2">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <span className="text-label font-medium flex items-center gap-1.5">
            <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
            Webhook signing secret
          </span>
          <p className="text-micro text-muted-foreground">
            Signs inbound webhook payloads for this agent. The stored value is never
            returned — rotating is the only way to obtain one, and it invalidates the
            previous secret immediately.
          </p>
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="h-7 shrink-0 gap-1.5 text-micro"
          disabled={!canRotate || rotating}
          onClick={rotate}
          title={canRotate ? undefined : "Requires manager or admin"}
        >
          {rotating ? <Spinner className="h-3 w-3" /> : <RefreshCw className="h-3 w-3" />}
          {rotating ? "Rotating…" : "Rotate"}
        </Button>
      </div>

      {secret && (
        <div className="rounded-md border border-amber-500/40 bg-amber-500/10 p-2 space-y-1.5">
          <p className="text-micro text-amber-600 dark:text-amber-400">
            Shown once. Copy it now — it cannot be retrieved again.
          </p>
          <div className="flex items-center gap-2">
            <code
              data-testid="webhook-secret-value"
              className="flex-1 min-w-0 truncate rounded bg-background/60 px-2 py-1 font-mono text-micro"
            >
              {secret}
            </code>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-7 w-7 p-0 shrink-0"
              onClick={copy}
              aria-label="Copy webhook secret"
            >
              <Copy className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
      )}

      {!canRotate && (
        <p className="text-micro text-muted-foreground">
          Requires a manager or admin to rotate.
        </p>
      )}
    </div>
  )
}
