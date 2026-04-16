"use client"

import * as React from "react"
import { Check, ExternalLink, Loader2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { StatusBadge } from "@/components/ui/status-badge"
import { cn } from "@/lib/utils"

interface OAuthAutoConnectProps {
  serverName: string
  mcpURL: string
  workspaceId: string | null
  authStatus: "connected" | "missing" | "expired" | "none"
  onCredentialCreated: (credId: string) => Promise<void>
}

/**
 * OAuth auto-discovery + consent flow for a single MCP integration.
 * Owns its own polling state so the parent only hears the final
 * "authorised → credential <id>" event via onCredentialCreated.
 */
export function OAuthAutoConnect({
  serverName,
  mcpURL,
  workspaceId,
  authStatus,
  onCredentialCreated,
}: OAuthAutoConnectProps) {
  const [status, setStatus] = React.useState<
    "idle" | "discovering" | "authorizing" | "polling" | "done" | "error"
  >(authStatus === "connected" ? "done" : "idle")
  const [error, setError] = React.useState("")
  const pollRef = React.useRef<ReturnType<typeof setInterval> | null>(null)
  const timeoutRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)
  // Guards against double-firing onCredentialCreated. If a poll
  // response is in flight when a second tick observes ACTIVE, both
  // would otherwise invoke the parent callback and duplicate agent
  // binding writes. Reset on every fresh handleConnect.
  const completedRef = React.useRef(false)

  React.useEffect(() => {
    return () => {
      if (pollRef.current) clearInterval(pollRef.current)
      if (timeoutRef.current) clearTimeout(timeoutRef.current)
    }
  }, [])

  async function handleConnect() {
    if (!workspaceId) return
    setStatus("discovering")
    setError("")
    completedRef.current = false

    try {
      const res = await fetch(`/api/v1/oauth/auto-connect?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mcp_url: mcpURL, server_name: serverName }),
      })
      const data = await res.json()

      if (data.status === "authorize") {
        // Open browser for OAuth consent FIRST — if the popup is
        // blocked, skip the authorizing-state dance entirely so the
        // user gets a concrete error instead of a 2-minute silent wait.
        const popup = window.open(data.auth_url, "_blank", "width=600,height=700")
        if (!popup) {
          setStatus("error")
          setError("Popup blocked. Allow popups for this site and try again.")
          return
        }
        setStatus("authorizing")

        // Poll credential status until ACTIVE
        const credId = data.credential_id
        pollRef.current = setInterval(async () => {
          try {
            const credRes = await fetch(`/api/v1/credentials/${credId}?workspace_id=${workspaceId}`)
            if (credRes.ok) {
              const cred = await credRes.json()
              if (cred.status === "ACTIVE" && !completedRef.current) {
                // Mark completed FIRST so a concurrent tick that
                // already fetched this response can't also fall
                // through to the callback.
                completedRef.current = true
                if (pollRef.current) clearInterval(pollRef.current)
                pollRef.current = null
                if (timeoutRef.current) clearTimeout(timeoutRef.current)
                timeoutRef.current = null
                setStatus("done")
                await onCredentialCreated(credId)
              }
            }
          } catch { /* keep polling */ }
        }, 2000)

        // Stop polling after 2 minutes. Stored in a ref so the cleanup
        // useEffect can clear it when the component unmounts — otherwise
        // the 2-minute timer would fire against unmounted state.
        timeoutRef.current = setTimeout(() => {
          if (pollRef.current) {
            clearInterval(pollRef.current)
            pollRef.current = null
            setStatus("error")
            setError("Authorization timed out. Please try again.")
          }
          timeoutRef.current = null
        }, 120000)
      } else if (data.status === "needs_client_id") {
        setStatus("error")
        setError(data.message || "Please provide Client ID manually via OAuth form in credential picker.")
      } else {
        setStatus("error")
        setError(data.error || "Unknown error")
      }
    } catch {
      setStatus("error")
      setError("Network error")
    }
  }

  if (status === "done" && authStatus !== "missing" && authStatus !== "expired") {
    return (
      <Card className="p-4 bg-surface-subtle">
        <div className="flex items-center gap-2 text-body font-medium">
          <Check className="h-4 w-4" />
          <StatusBadge status="COMPLETED" label="OAuth connected" />
        </div>
      </Card>
    )
  }

  const isMissing = authStatus === "missing"
  const isExpired = authStatus === "expired"

  return (
    <Card
      className={cn(
        "p-4 space-y-3",
        isMissing && "border-destructive/50 bg-destructive/5",
        isExpired && "border-amber-500/50 bg-amber-500/5",
      )}
    >
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-body font-medium">
          <ExternalLink className="h-4 w-4 text-muted-foreground" />
          Authentication
        </div>
        {isMissing && (
          <StatusBadge status="FAILED" label="Credential missing" />
        )}
        {isExpired && (
          <StatusBadge status="BLOCKED" label="Expired" />
        )}
      </div>
      <p className="text-label text-muted-foreground">
        {isMissing
          ? "The credential for this integration was deleted. Reconnect to restore access."
          : isExpired
            ? "The OAuth token has expired. Reconnect to refresh."
            : "Connect with OAuth to automatically authenticate with this service."}
      </p>
      {error && (
        <p className="text-label text-destructive">{error}</p>
      )}
      <Button
        size="sm"
        variant={isMissing || isExpired ? "destructive" : "default"}
        onClick={handleConnect}
        disabled={status === "discovering" || status === "authorizing" || status === "polling"}
      >
        {(status === "discovering" || status === "authorizing") && (
          <Loader2 className="mr-2 h-3 w-3 animate-spin" />
        )}
        {status === "authorizing" ? "Waiting for authorization..."
          : isMissing || isExpired ? "Reconnect with OAuth"
          : "Connect with OAuth"}
      </Button>
    </Card>
  )
}
