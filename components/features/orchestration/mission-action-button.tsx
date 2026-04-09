"use client"

import { useCallback, useState } from "react"
import { Play, Square, Loader2 } from "lucide-react"
import { toast } from "sonner"
import type { Mission } from "@/lib/types/mission"

/** Compact action button for the toolbar (Start/Cancel) */
export function MissionActionButton({ mission, action, workspaceId, onDone }: {
  mission: Mission; action: "start" | "cancel"; workspaceId: string; onDone: () => void
}) {
  const [loading, setLoading] = useState(false)
  const handleClick = useCallback(async () => {
    setLoading(true)
    try {
      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
      const res = action === "start"
        ? await fetch(`/api/v1/crews/${mission.crew_id}/missions/${mission.id}/start${qs}`, { method: "POST" })
        : await fetch(`/api/v1/crews/${mission.crew_id}/missions/${mission.id}${qs}`, {
            method: "PATCH", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ status: "CANCELLED" }),
          })
      if (!res.ok) { const b = await res.json().catch(() => null); toast.error(b?.detail ?? `Failed to ${action}`); return }
      toast.success(action === "start" ? "Mission started" : "Mission cancelled")
      onDone()
    } catch { toast.error(`Failed to ${action}`) } finally { setLoading(false) }
  }, [mission.id, mission.crew_id, workspaceId, action, onDone])

  if (action === "start") {
    return (
      <button onClick={handleClick} disabled={loading} className="inline-flex items-center gap-1 h-[22px] px-2 rounded-[3px] text-[11.5px] font-medium bg-blue-500/15 border border-blue-500/35 text-blue-400 hover:bg-blue-500/25 transition-colors disabled:opacity-50">
        {loading ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
        Start
      </button>
    )
  }
  return (
    <button onClick={handleClick} disabled={loading} className="inline-flex items-center gap-1 h-[22px] px-2 rounded-[3px] text-[11.5px] font-medium bg-red-500/10 border border-red-500/30 text-red-400 hover:bg-red-500/20 transition-colors disabled:opacity-50">
      {loading ? <Loader2 className="h-3 w-3 animate-spin" /> : <Square className="h-3 w-3" />}
      Cancel
    </button>
  )
}
