"use client"

import { useEffect, useRef, useState } from "react"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { CreateSurface, CreateSurfaceHeader, CreateSurfaceBody } from "@/components/layout/create-surface"
import { ChatDocumentAccess } from "./right-panel-tabs/chat-document-access"

interface Props { workspaceId: string; chatId: string; messageId: string; authorId: string }

/** The history ID/author select an original message; the server rechecks both
 * and consumes its existing document delegation before accepting any work. */
export function RunInDocument(props: Props) {
  const viewerId = useSessionSafe().data?.user?.id
  if (!viewerId || viewerId !== props.authorId) return null
  return <RunScope key={JSON.stringify([viewerId, props.workspaceId, props.chatId, props.messageId])} {...props} />
}

function RunScope({ workspaceId, chatId, messageId, authorId: propsAuthorId }: Props) {
  const [open, setOpen] = useState(false)
  const [error, setError] = useState(false)
  const [pending, setPending] = useState(false)
  const [requested, setRequested] = useState(false)
  const [opener, setOpener] = useState<HTMLElement | null>(null)
  const active = useRef(true)
  const submit = useRef<AbortController | null>(null)
  useEffect(() => {
    active.current = true
    return () => { active.current = false; submit.current?.abort() }
  }, [])
  const run = async (pageId: string) => {
    if (pending) return
    setPending(true); setError(false)
    const controller = new AbortController(); submit.current = controller
    const timeout = setTimeout(() => controller.abort(), 5000)
    try {
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/mooring/tasks?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ chat_id: chatId, message_id: messageId }), signal: controller.signal, cache: "no-store" })
      if (!response.ok || response.status !== 202) throw new Error("not accepted")
      if (active.current && !controller.signal.aborted) { setRequested(true) }
    } catch {
      if (active.current) { setError(true) }
    } finally {
      clearTimeout(timeout)
      if (active.current) setPending(false)
    }
  }
  return <>
    <Button variant="ghost" size="sm" onClick={event => { setOpener(event.currentTarget); setOpen(true) }}>Run in this Doc</Button>
    <CreateSurface open={open} onOpenChange={setOpen} size="sm" ariaLabel="Run message in a document" restoreFocusTo={opener}>
      <CreateSurfaceHeader title="Run in this Doc" description="Use this original message in one document. This uses the agent’s existing access; it does not grant access." onClose={() => setOpen(false)} />
      <CreateSurfaceBody>
        {requested ? <p role="status">Task requested. Saved changes will appear in the document.</p>
          : <>
            {error && <p role="alert">The task or current document access could not be confirmed. No successful save is reported.</p>}
            {open && <ChatDocumentAccess workspaceId={workspaceId} chatId={chatId} viewerId={propsAuthorId} choicePending={pending} chooseDocument={pageId => { void run(pageId) }} />}
          </>}
      </CreateSurfaceBody>
    </CreateSurface>
  </>
}
