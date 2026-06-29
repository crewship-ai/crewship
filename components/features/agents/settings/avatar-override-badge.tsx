"use client"

import { useState } from "react"
import { AlertCircle, RotateCcw } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"

interface AvatarOverrideBadgeProps {
  agentId: string
  workspaceId: string
  hasOverride: boolean
  onReset: () => void
}

/**
 * Shown next to the agent Avatar section when `agent.avatar_style` is set,
 * i.e. it overrides the crew-level default. One-click reset clears the
 * per-agent value so the crew style takes over again.
 */
export function AvatarOverrideBadge({ agentId, workspaceId, hasOverride, onReset }: AvatarOverrideBadgeProps) {
  const [loading, setLoading] = useState(false)

  if (!hasOverride) return null

  const handleReset = async () => {
    setLoading(true)
    try {
      const res = await apiFetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ avatar_style: null }),
      })
      if (!res.ok) throw new Error("Failed")
      toast.success("Reverted to crew default avatar style")
      onReset()
    } catch {
      toast.error("Couldn't reset avatar style")
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 p-3">
      <AlertCircle className="h-4 w-4 text-muted-foreground mt-0.5 shrink-0" />
      <div className="flex-1 min-w-0">
        <p className="text-label font-medium">Custom avatar style</p>
        <p className="text-micro text-muted-foreground mt-0.5">
          This agent uses its own style instead of the crew default.
        </p>
      </div>
      <Button
        variant="outline"
        size="sm"
        className="h-7 text-micro gap-1 shrink-0"
        onClick={handleReset}
        disabled={loading}
      >
        {loading ? <Spinner className="h-3 w-3" /> : <RotateCcw className="h-3 w-3" />}
        Reset to crew
      </Button>
    </div>
  )
}
