"use client"

import { createContext, useContext, useEffect, useState, type ReactNode } from "react"
import { Send } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useArtifactRequests, type ArtifactAgentRequest, type ArtifactRequestDraft } from "@/hooks/use-artifact-requests"
import { ArtifactRequestDialog } from "./artifact-request-dialog"
import { ArtifactRequestStatus, ArtifactRequestsPanel } from "./artifact-requests-panel"

interface RequestSource { base_rev: number; thread_id?: string; instruction?: string; chat_id?: string }
interface AgentWorkContext {
  service: ReturnType<typeof useArtifactRequests>
  revision: number
  head: number
  canDispatch: boolean
  pending: boolean
  acceptedId: string | null
  open: (source: RequestSource) => void
  viewRevision: (revision: number) => void
}
const AgentWorkContext = createContext<AgentWorkContext | null>(null)
export function useArtifactAgentWork() { return useContext(AgentWorkContext) }

export function ArtifactAgentWorkProvider({ workspaceId, slug, revision, head, onViewRevision, onDirtyChange, children }: {
  workspaceId: string
  slug: string
  revision: number
  head: number
  onViewRevision: (revision: number) => void
  onDirtyChange: (dirty: boolean) => void
  children: ReactNode
}) {
  const service = useArtifactRequests(workspaceId, slug)
  const [draft, setDraft] = useState<ArtifactRequestDraft | null>(null)
  const [open, setOpen] = useState(false)
  const [acceptedId, setAcceptedId] = useState<string | null>(null)
  const canDispatch = service.query.data?.can_dispatch === true
  const pending = service.send.isPending || service.cancel.isPending
  const dirty = Boolean(draft?.instruction.trim())
  useEffect(() => { onDirtyChange(dirty); return () => onDirtyChange(false) }, [dirty, onDirtyChange])
  useEffect(() => {
    if (service.query.data?.can_dispatch === false || (service.query.error && !service.query.data)) {
      if (service.query.error && !("status" in service.query.error)) return
      setDraft(null); setOpen(false)
    }
  }, [service.query.data?.can_dispatch, service.query.error, service.query.data])
  const start = (source: RequestSource) => {
    if (!canDispatch || source.base_rev !== head) return
    // Closing with Escape retains the private draft. Another entry point must
    // reopen that same draft rather than silently changing its revision/thread.
    if (!draft?.instruction.trim()) {
      setDraft({ intent_id: crypto.randomUUID(), chat_id: source.chat_id ?? "", instruction: source.instruction ?? "", base_rev: source.base_rev, ...(source.thread_id ? { thread_id: source.thread_id } : {}) })
      service.send.reset()
    }
    setOpen(true)
  }
  return <AgentWorkContext.Provider value={{ service, revision, head, canDispatch, pending, acceptedId, open: start, viewRevision: onViewRevision }}>
    {children}
    {draft && <ArtifactRequestDialog
      open={open} onOpenChange={value => { if (!pending) setOpen(value) }}
      draft={draft} head={head} targets={service.query.data?.eligible_chats ?? []}
      pending={pending} error={service.send.error?.message ?? null}
      onChange={setDraft} onDiscard={() => { setDraft(null); setOpen(false); service.send.reset() }}
      onRefresh={() => void service.query.refetch()}
      onSend={async () => {
        try {
          const receipt = await service.send.mutateAsync(draft)
          setAcceptedId(receipt.id); setDraft(null); setOpen(false)
        } catch { /* Keep the exact draft and submission identity for retry. */ }
      }}
    />}
  </AgentWorkContext.Provider>
}

export function ArtifactSendButton({ thread }: { thread?: { id: string; revision: number } }) {
  const work = useArtifactAgentWork()
  if (!work?.canDispatch) return null
  const source = thread?.revision ?? work.revision
  const historical = source !== work.head
  return <div className="flex flex-wrap items-center gap-2">
    <Button size="sm" variant="outline" className="coarse:h-12" disabled={work.pending || historical} onClick={() => work.open({ base_rev: source, ...(thread ? { thread_id: thread.id } : {}) })}>
      <Send className="mr-1.5 h-4 w-4" />Send to agent
    </Button>
    {historical && <span className="type-page-meta text-muted-foreground">Requests require latest revision {work.head}.</span>}
  </div>
}

export function ArtifactAgentRequests() {
  const work = useArtifactAgentWork()
  if (!work) return null
  return <ArtifactRequestsPanel
    data={work.service.query.data} error={work.service.query.error?.message ?? null}
    writeError={work.service.cancel.error?.message ?? null} loading={work.service.query.isPending}
    pending={work.pending} head={work.head} acceptedId={work.acceptedId}
    onRefresh={() => { work.service.cancel.reset(); void work.service.query.refetch() }}
    onCancel={async id => { try { await work.service.cancel.mutateAsync(id) } catch { /* Server state is refreshed; never invent cancellation. */ } }}
    onRetry={(request: ArtifactAgentRequest) => work.open({ base_rev: request.base_rev, thread_id: request.thread_id, instruction: request.instruction, chat_id: request.chat_id })}
    onViewRevision={work.viewRevision}
  />
}

export function ArtifactThreadRequests({ threadId }: { threadId: string }) {
  const work = useArtifactAgentWork()
  const requests = work?.service.query.data?.requests.filter(item => item.thread_id === threadId) ?? []
  if (!work || !requests.length) return null
  return <div className="space-y-2 border-t pt-3" aria-label="Thread agent requests">
    {requests.slice(0, 3).map(request => <div key={request.id} className="flex flex-wrap items-center gap-2 text-xs">
      <span>Agent request</span><ArtifactRequestStatus request={request} />
      {request.status === "succeeded" && request.result_revision && <Button size="sm" variant="ghost" className="coarse:h-12" onClick={() => work.viewRevision(request.result_revision!)}>View result revision {request.result_revision}</Button>}
    </div>)}
  </div>
}
