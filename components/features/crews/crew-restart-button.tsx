"use client"

import { useState } from "react"
import { RotateCcw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"

export function CrewRestartButton({ workspaceId, crewId, name, onRestart }: { workspaceId: string; crewId: string; name: string; onRestart: () => void }) {
  const { abilities } = useAbilities()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<string | null>(null)
  if (!abilities.can("update", "Crew")) return null
  async function restart() {
    setBusy(true); setMessage("Recycling crew container…")
    try {
      const r = await apiFetch(`/api/v1/crews/${encodeURIComponent(crewId)}/restart-agents?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
      if (!r.ok) throw new Error(`Container could not be recycled (${r.status}). You can retry.`)
      setMessage("Crew runtime reset completed. A fresh container will start on the next agent run.")
      setOpen(false); onRestart()
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Container could not be recycled.")
      throw error
    } finally { setBusy(false) }
  }
  return <div className="space-y-2"><Button variant="outline" size="sm" onClick={() => setOpen(true)} disabled={busy}><RotateCcw className={busy ? "animate-spin" : ""} />{busy ? "Recycling…" : "Restart container"}</Button>{message && <p role="status" className="max-w-sm text-xs text-muted-foreground">{message}</p>}<ConfirmDialog open={open} onOpenChange={setOpen} title={`Restart ${name} container?`} description="This stops the shared container and can interrupt work by every agent in this crew. The next agent run creates a fresh container; it does not start immediately." confirmLabel="Restart container" onConfirm={restart} /></div>
}
