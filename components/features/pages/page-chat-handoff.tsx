"use client"

import * as React from "react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"

type ChatTarget = { id: string; slug: string; name: string; agent_role?: string | null; expired_at?: string | null; crew?: { slug?: string | null } | null }

export function pageChatHandoffHref(agentSlug: string, pageSlug: string, workspaceId: string) {
  return `/chat/${encodeURIComponent(agentSlug)}?${new URLSearchParams({ page: pageSlug, new: "1", workspace_id: workspaceId })}`
}

export function PageChatHandoff({ open, onOpenChange, workspaceId, pageSlug, ownerCrewSlug }: {
  open: boolean; onOpenChange: (open: boolean) => void; workspaceId: string; pageSlug: string; ownerCrewSlug?: string | null
}) {
  const [agents, setAgents] = React.useState<ChatTarget[]>([])
  const [selectedId, setSelectedId] = React.useState("")
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState(false)
  React.useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    setLoading(true)
    setError(false)
    setAgents([])
    setSelectedId("")
    apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
      .then((r) => r.ok ? r.json() : Promise.reject(new Error(`Agents ${r.status}`)))
      .then((rows: unknown) => {
        if (controller.signal.aborted) return
        const live = Array.isArray(rows) ? rows.filter((a): a is ChatTarget =>
          a && typeof a.id === "string" && typeof a.slug === "string" && typeof a.name === "string" && !a.expired_at) : []
        setAgents(live)
        const lead = live.find((a) => a.agent_role === "LEAD" && a.crew?.slug === ownerCrewSlug)
        if (lead) setSelectedId(lead.id)
      })
      .catch(() => { if (!controller.signal.aborted) setError(true) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [open, workspaceId, ownerCrewSlug])
  const selected = agents.find((a) => a.id === selectedId)
  return <Dialog open={open} onOpenChange={onOpenChange}>
    <DialogContent>
      <DialogHeader>
        <DialogTitle>Ask about this Page</DialogTitle>
        <DialogDescription>Choose an agent with a Page grant. Access for both of you is checked again when you send a message.</DialogDescription>
      </DialogHeader>
      {loading ? <p role="status">Loading agents…</p> : error ? <p role="alert">Agents could not be loaded.</p> : agents.length === 0 ? <p>No agents are available.</p> : <label className="block text-sm">
        Agent
        <select className="mt-1 w-full rounded-md border border-border bg-background p-2" value={selectedId} onChange={(e) => setSelectedId(e.target.value)}>
          <option value="">Choose an agent</option>
          {agents.map((a) => <option value={a.id} key={a.id}>{a.name}{a.agent_role === "LEAD" ? " · Lead" : ""}</option>)}
        </select>
      </label>}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
        {selected ? <Button asChild><a href={pageChatHandoffHref(selected.slug, pageSlug, workspaceId)}>Open draft</a></Button> : <Button disabled>Open draft</Button>}
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
