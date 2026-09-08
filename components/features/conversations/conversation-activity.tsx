"use client"

import { useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Activity } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { conversationRequest } from "@/hooks/use-workspace-conversations"

type ActivitySettings = { issues: boolean; routines: boolean }
export function ConversationActivity({ workspaceId, userId, conversationId, canManage }: { workspaceId: string; userId: string; conversationId: string; canManage: boolean }) {
  const client = useQueryClient()
  const key = ["workspace-conversations", workspaceId, userId, conversationId, "activity"]
  const settings = useQuery({ queryKey: key, queryFn: ({ signal }) => conversationRequest<ActivitySettings>(workspaceId, `conversations/${encodeURIComponent(conversationId)}/activity`, undefined, signal) })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(false)
  async function change(field: keyof ActivitySettings, checked: boolean) {
    if (!settings.data || busy) return
    setBusy(true); setError(false)
    try {
      const response = await apiFetch(`/api/v1/conversations/${encodeURIComponent(conversationId)}/activity?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ issues: field === "issues" ? checked : settings.data.issues, routines: field === "routines" ? checked : settings.data.routines }) })
      if (!response.ok) throw new Error("save failed")
      await client.invalidateQueries({ queryKey: key })
    } catch { setError(true) }
    finally { setBusy(false) }
  }
  return <section className="space-y-3 border-t pt-3"><h3 className="flex items-center gap-2 text-sm font-medium"><Activity className="size-4" />Workspace activity</h3><p className="text-xs text-muted-foreground">Post new issue updates and routine results here. Earlier activity is not imported. {canManage ? "" : "The channel creator manages these subscriptions."}</p>{settings.isPending && <p className="text-xs">Loading subscriptions…</p>}{settings.data && ([['issues', 'Issue updates'], ['routines', 'Routine results']] as const).map(([field, label]) => <label key={field} className="flex min-h-9 items-center gap-3 text-sm"><input type="checkbox" checked={settings.data[field]} disabled={!canManage || busy} onChange={(e) => { void change(field, e.target.checked) }} />{label}</label>)}{settings.error && <p role="alert" className="text-xs">Unable to load activity subscriptions. <button type="button" className="underline" onClick={() => { void settings.refetch() }}>Retry</button></p>}{error && <p role="alert" className="text-xs text-destructive">Unable to save activity subscriptions. Please try again.</p>}</section>
}
