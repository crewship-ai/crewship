"use client"

import * as React from "react"
import { AlertTriangle, Check, Info, Loader2, XCircle, Zap } from "lucide-react"

import { Button } from "@/components/ui/button"
import { StatusBadge } from "@/components/ui/status-badge"
import type { TestResult } from "./types"

interface TestConnectionButtonProps {
  serverId: string
  crewId: string
  workspaceId: string | null
}

/**
 * Inline control that calls the crew-scoped MCP test endpoint and
 * renders the verdict as a StatusBadge. Result auto-clears after 10s
 * so the row returns to its idle state without a manual dismiss.
 */
export function TestConnectionButton({
  serverId,
  crewId,
  workspaceId,
}: TestConnectionButtonProps) {
  const [testing, setTesting] = React.useState(false)
  const [result, setResult] = React.useState<TestResult | null>(null)
  const timerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null)

  React.useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  async function handleTest() {
    if (!workspaceId) return
    setTesting(true)
    setResult(null)
    if (timerRef.current) clearTimeout(timerRef.current)

    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/integrations/${serverId}/test?workspace_id=${workspaceId}`,
        { method: "POST" },
      )
      if (!res.ok) {
        const errData = await res.json().catch(() => null)
        setResult({ status: "error", message: errData?.error || `HTTP ${res.status}` })
      } else {
        const data: TestResult = await res.json()
        setResult(data)
      }

      timerRef.current = setTimeout(() => {
        setResult(null)
        timerRef.current = null
      }, 10000)
    } catch {
      setResult({ status: "error", message: "Network error" })
      timerRef.current = setTimeout(() => {
        setResult(null)
        timerRef.current = null
      }, 10000)
    } finally {
      setTesting(false)
    }
  }

  return (
    <div className="flex items-center gap-3">
      <Button
        variant="outline"
        size="sm"
        className="h-8 text-label"
        onClick={handleTest}
        disabled={testing}
      >
        {testing ? (
          <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
        ) : (
          <Zap className="mr-1.5 h-3.5 w-3.5" />
        )}
        Test Connection
      </Button>
      {result && (
        <span className="inline-flex items-center gap-1.5">
          {result.status === "ok" && (
            <StatusBadge status="COMPLETED" label={<span className="inline-flex items-center gap-1"><Check className="h-3 w-3" />Connected</span>} />
          )}
          {result.status === "auth_required" && (
            <StatusBadge status="BLOCKED" label={<span className="inline-flex items-center gap-1"><AlertTriangle className="h-3 w-3" />Authentication required</span>} />
          )}
          {result.status === "error" && (
            <StatusBadge status="FAILED" label={<span className="inline-flex items-center gap-1"><XCircle className="h-3 w-3" />{result.message || "Connection failed"}</span>} />
          )}
          {result.status === "skipped" && (
            <span className="inline-flex items-center gap-1.5 text-label text-muted-foreground">
              <Info className="h-3.5 w-3.5" />
              Tested at runtime
            </span>
          )}
        </span>
      )}
    </div>
  )
}
