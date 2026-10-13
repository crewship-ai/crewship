"use client"

import { useState } from "react"
import { Ban, CheckCircle2, CircleAlert, Clock, Loader2, XCircle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusIconBadge } from "@/components/ui/status-icon-badge"
import { STATUS_STYLES, type StatusConfigEntryWithIcon } from "@/lib/status-config"
import type { ArtifactAgentRequest, ArtifactRequestsResult } from "@/hooks/use-artifact-requests"

const STATUS: Record<ArtifactAgentRequest["status"], StatusConfigEntryWithIcon> = {
  queued: { label: "Queued", icon: Clock, className: STATUS_STYLES.amber },
  running: { label: "Running", icon: Loader2, className: STATUS_STYLES.blue },
  succeeded: { label: "Revision published", icon: CheckCircle2, className: STATUS_STYLES.emerald },
  failed: { label: "Failed", icon: XCircle, className: STATUS_STYLES.red },
  cancelled: { label: "Cancelled", icon: Ban, className: STATUS_STYLES.gray },
}
export function ArtifactRequestStatus({ request }: { request: ArtifactAgentRequest }) {
  const entry = request.status === "succeeded" && !request.result_revision
    ? { label: "Result unavailable", icon: CircleAlert, className: STATUS_STYLES.amber }
    : STATUS[request.status] ?? { label: "Unknown state", icon: CircleAlert, className: STATUS_STYLES.gray }
  return <StatusIconBadge entry={entry} />
}
export function ArtifactRequestsPanel({ data, error, writeError, loading, pending, head, acceptedId, onRefresh, onCancel, onRetry, onViewRevision }: {
  data: ArtifactRequestsResult | undefined
  error: string | null
  writeError: string | null
  loading: boolean
  pending: boolean
  head: number
  acceptedId: string | null
  onRefresh: () => void
  onCancel: (id: string) => void
  onRetry: (request: ArtifactAgentRequest) => void
  onViewRevision: (revision: number) => void
}) {
  const [all, setAll] = useState(false)
  if (!data && error) return <div role="alert" className="space-y-2 border-t p-4 text-sm text-destructive"><p>{error}</p><Button size="sm" variant="outline" onClick={onRefresh}>Retry agent requests</Button></div>
  if (!data && loading) return <p role="status" className="border-t p-4 text-sm text-muted-foreground">Loading agent requests…</p>
  if (!data) return null
  const visible = all ? data.requests : data.requests.slice(0, 5)
  return <section aria-label="Agent requests" className="space-y-3 border-t p-4 md:p-6">
    <div className="flex flex-wrap items-center gap-2"><h2 className="mr-auto text-sm font-semibold">Agent requests</h2><Button size="sm" variant="ghost" className="coarse:h-12" disabled={pending} onClick={onRefresh}>Refresh requests</Button></div>
    <p className="text-xs text-muted-foreground">Explicit requests only. A thread stays open until a person resolves it. Showing up to the latest 100 requests.</p>
    {!data.can_dispatch && <p className="text-xs text-muted-foreground">Edit permission is required to send requests or cancel runs.</p>}
    {error && <p role="alert" className="text-sm text-warn">Request status could not refresh. Showing the last authorized snapshot. {error}</p>}
    {writeError && <p role="alert" className="text-sm text-destructive">{writeError}</p>}
    {acceptedId && !data.requests.some(item => item.id === acceptedId) && <p role="status" className="text-sm" title={acceptedId}>Request accepted. Refresh to see its current status.</p>}
    {!data.requests.length && <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">No agent requests yet. Use Send to agent to review the target and instructions before starting work.</p>}
    <ol className="space-y-3">{visible.map(request => {
      const target = data.eligible_chats.find(item => item.chat_id === request.chat_id)
      const name = request.agent_name || target?.agent_name || "Agent"
      const agentSlug = target?.agent_slug
      const active = ["queued", "running"].includes(request.status)
      const retryable = ["failed", "cancelled"].includes(request.status)
      return <li key={request.id} className="space-y-3 rounded-lg border bg-background p-4">
        <div className="flex flex-wrap items-center gap-2"><span className="text-sm font-medium" title={request.agent_id}>{name}</span><ArtifactRequestStatus request={request} /><span className="type-page-meta text-muted-foreground">Source revision {request.base_rev}{request.thread_id ? " · Thread snapshot" : " · Whole document"}</span></div>
        <time className="block text-xs text-muted-foreground" dateTime={request.updated_at}>Updated {new Date(request.updated_at).toLocaleString()}</time>
        {request.instruction && <details className="text-sm"><summary className="cursor-pointer text-muted-foreground">Instructions sent</summary><p className="mt-2 whitespace-pre-wrap break-words">{request.instruction}</p></details>}
        {request.error && <p className="whitespace-pre-wrap text-sm text-destructive">{request.error}</p>}
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="ghost" className="coarse:h-12" onClick={() => onViewRevision(request.base_rev)}>View source revision {request.base_rev}</Button>
          {request.status === "succeeded" && request.result_revision && <Button size="sm" variant="outline" className="coarse:h-12" onClick={() => onViewRevision(request.result_revision!)}>View result revision {request.result_revision}</Button>}
          {agentSlug && <Button asChild size="sm" variant="ghost" className="coarse:h-12"><a href={`/chat/${encodeURIComponent(agentSlug)}?${new URLSearchParams({ session: request.chat_id })}`}>Open agent chat</a></Button>}
          {data.can_dispatch && active && <Button size="sm" variant="outline" className="coarse:h-12" disabled={pending} onClick={() => onCancel(request.id)}>Cancel request</Button>}
          {data.can_dispatch && retryable && request.instruction && <Button size="sm" variant="outline" className="coarse:h-12" disabled={pending || request.base_rev !== head} onClick={() => onRetry(request)}>Retry as new request</Button>}
        </div>
        {data.can_dispatch && retryable && request.base_rev !== head && <p className="text-xs text-muted-foreground">The source revision is no longer latest. Review revision {head} and start a new request; this request’s snapshot will not move.</p>}
      </li>
    })}</ol>
    {data.requests.length > 5 && <Button variant="ghost" size="sm" onClick={() => setAll(value => !value)}>{all ? "Show fewer requests" : `Show ${data.requests.length} requests`}</Button>}
  </section>
}
