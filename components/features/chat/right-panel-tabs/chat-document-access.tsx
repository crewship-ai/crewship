"use client"

import { useCallback, useEffect, useState } from "react"
import { QueryClient, QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

interface TaskView {
  work_id: string
  run_id: string
  scope: string
  expires_at: string
  can_read: boolean
  can_write: boolean
}
interface DocumentAccess {
  page_id: string
  title: string
  document_type: string
  document_grant: "read" | "write"
  policy_allows: boolean
  task_status: "unavailable" | "no_active_task" | "active"
  tasks: TaskView[]
  provenance: "unknown"
}
const freshnessMs = 3000

export const chatDocumentAccessKeys = {
  view: (workspaceId: string, chatId: string, viewerId: string) => ["chat-document-access", workspaceId, chatId, viewerId] as const,
}

interface AccessProps { workspaceId: string; chatId: string; viewerId: string }

function AccessScope(props: AccessProps) {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } } }))
  useEffect(() => () => { client.clear() }, [client])
  return <QueryClientProvider client={client}><AccessView {...props} /></QueryClientProvider>
}

/** Display metadata only. Opening a document still uses its ordinary ACL. */
export function ChatDocumentAccess(props: AccessProps) {
  return <AccessScope key={JSON.stringify([props.workspaceId, props.chatId, props.viewerId])} {...props} />
}

function AccessView({ workspaceId, chatId, viewerId }: AccessProps) {
  const client = useQueryClient()
  const [visible, setVisible] = useState(() => typeof document === "undefined" || document.visibilityState !== "hidden")
  const [expired, setExpired] = useState(false)
  const query = useQuery({
    queryKey: chatDocumentAccessKeys.view(workspaceId, chatId, viewerId),
    enabled: visible,
    retry: false,
    gcTime: 0,
    staleTime: 0,
    structuralSharing: false,
    // Original credential/attempt expiry has no complete realtime event source.
    // This bounded authority view must recheck within the revocation guarantee.
    refetchInterval: 1500,
    refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const started = performance.now()
      const controller = new AbortController()
      const abort = () => controller.abort()
      signal.addEventListener("abort", abort, { once: true })
      if (signal.aborted) controller.abort()
      const timeout = setTimeout(abort, freshnessMs)
      try {
        const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/document-access?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal, cache: "no-store" })
        if (!response.ok) throw new Error("Access unavailable")
        const data = await response.json() as { documents: DocumentAccess[] }
        if (controller.signal.aborted || performance.now() - started >= freshnessMs || !Array.isArray(data.documents)) throw new Error("Access unavailable")
        return { documents: data.documents, expiresAt: started + freshnessMs }
      } finally {
        clearTimeout(timeout)
        signal.removeEventListener("abort", abort)
      }
    },
  })
  const invalidate = useCallback(() => {
    void client.invalidateQueries({ queryKey: chatDocumentAccessKeys.view(workspaceId, chatId, viewerId) })
  }, [client, workspaceId, chatId, viewerId])
  useRealtimeEventSafe("conversation.updated", invalidate)
  useRealtimeEventSafe("run.started", invalidate)
  useRealtimeEventSafe("run.completed", invalidate)
  useRealtimeEventSafe("run.failed", invalidate)
  useRealtimeEventSafe("workspace.updated", invalidate)
  useRealtimeEventSafe("agent.updated", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)

  useEffect(() => {
    const visibility = () => {
      setVisible(document.visibilityState !== "hidden")
      void client.resetQueries({ queryKey: chatDocumentAccessKeys.view(workspaceId, chatId, viewerId) })
    }
    document.addEventListener("visibilitychange", visibility)
    return () => document.removeEventListener("visibilitychange", visibility)
  }, [client, workspaceId, chatId, viewerId])
  useEffect(() => {
    setExpired(false)
    if (!query.data) return
    const expiry = setTimeout(() => setExpired(true), Math.max(0, query.data.expiresAt - performance.now()))
    return () => clearTimeout(expiry)
  }, [query.data])

  const documents = visible && !expired && !query.isError && query.data && performance.now() < query.data.expiresAt ? query.data.documents : null
  const unavailable = query.isError || expired
  return <section aria-label="Agent document access" className="mb-4 space-y-2 border-b pb-3">
    <h3 className="eyebrow">Agent document access</h3>
    <p className="text-muted-foreground">Document grants and this chat’s active tasks are shown separately. Change access in the document’s Share menu.</p>
    {!documents ? <p role="status" className="text-muted-foreground">{unavailable ? "Current access could not be verified." : "Checking current access…"}</p>
      : documents.length === 0 ? <p className="text-muted-foreground">No document grants visible to you.</p>
      : <ul className="space-y-2">{documents.map((doc) => <li key={doc.page_id} className="rounded-xl border border-border bg-card p-2.5">
        <a className="font-medium text-primary underline" href={`/pages/${encodeURIComponent("@" + doc.page_id)}`}>{doc.title}</a>
        <p>Document grant: {doc.document_grant === "write" ? "read and write" : "read only"}</p>
        {!doc.policy_allows && <p className="text-muted-foreground">Agent access is blocked by workspace policy.</p>}
        {doc.task_status === "active" && doc.policy_allows && doc.tasks.some((task) => Date.parse(task.expires_at) > Date.now())
          ? doc.tasks.filter((task) => Date.parse(task.expires_at) > Date.now()).map((task) => <div key={task.run_id}>
            <p>Current task: {task.can_write ? "read and write" : "read only"} · this document</p>
            <p className="break-all text-muted-foreground">Task {task.work_id} · valid until <time dateTime={task.expires_at}>{new Date(task.expires_at).toLocaleString()}</time></p>
          </div>)
          : <p className="text-muted-foreground">{doc.task_status === "unavailable" ? "Current task permissions are unavailable." : "No verified active task in this chat."}</p>}
        <p className="text-muted-foreground">Origin: unknown</p>
      </li>)}</ul>}
  </section>
}
