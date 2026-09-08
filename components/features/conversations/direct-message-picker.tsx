"use client"

import { ConversationIdentity } from "./conversation-identity"
import { useEffect, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { conversationRequest, type ConversationPerson, type WorkspaceConversation } from "@/hooks/use-workspace-conversations"

export function DirectMessagePicker({ workspaceId, userId, onOpened }: { workspaceId: string; userId: string; onOpened: (conversation: WorkspaceConversation) => void }) {
  const [search, setSearch] = useState("")
  const [opening, setOpening] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const inFlight = useRef(false)
  const activeRequest = useRef<AbortController | null>(null)
  useEffect(() => () => { activeRequest.current?.abort() }, [])
  const people = useQuery({
    queryKey: ["workspace-conversation-people", workspaceId, userId],
    enabled: !!workspaceId && !!userId,
    queryFn: ({ signal }) => conversationRequest<{ user: ConversationPerson }[]>(workspaceId, `workspaces/${encodeURIComponent(workspaceId)}/members`, undefined, signal),
  })
  const colleagues = people.data?.filter(({ user }) => user.id !== userId)
  const matching = colleagues?.filter(({ user }) => `${user.full_name ?? ""} ${user.email}`.toLowerCase().includes(search.trim().toLowerCase()))
  async function open(personId: string) {
    if (inFlight.current || personId === userId) return
    inFlight.current = true
    const controller = new AbortController()
    activeRequest.current = controller
    setOpening(personId); setError(null)
    try {
      const conversation = await conversationRequest<WorkspaceConversation>(workspaceId, "conversations/direct", { user_id: personId }, controller.signal)
      if (controller.signal.aborted) return
      onOpened(conversation)
    } catch {
      if (controller.signal.aborted) return
      setError("Unable to open this direct message. Choose the person again to retry; an existing conversation will be reused.")
    } finally { inFlight.current = false; setOpening(null) }
  }
  return <div className="space-y-3">
    <Input autoFocus aria-label="Find a colleague" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search people by name or email…" />
    {people.isPending && <p role="status" className="text-sm text-muted-foreground">Loading workspace people…</p>}
    {people.error && <div role="alert" className="space-y-2 text-sm"><p>Unable to load workspace people.</p><Button type="button" variant="outline" onClick={() => { void people.refetch() }}>Retry loading people</Button></div>}
    {!people.isError && colleagues?.length === 0 && <p className="text-sm text-muted-foreground">There are no other people in this workspace yet.</p>}
    {!people.isError && !!colleagues?.length && matching?.length === 0 && <p className="text-sm text-muted-foreground">No people match your search.</p>}
    {!people.isError && <div className="max-h-[50dvh] space-y-1 overflow-y-auto" aria-label="Workspace colleagues">
      {matching?.map(({ user }) => <Button key={user.id} type="button" variant="ghost" disabled={opening !== null} onClick={() => { void open(user.id) }} className="h-auto min-h-12 w-full justify-start whitespace-normal px-3 py-2 text-left"><ConversationIdentity id={user.id} name={user.full_name || user.email} avatarUrl={user.avatar_url} /><span className="min-w-0"><span className="block break-words">{user.full_name || user.email}</span>{user.full_name && <span className="block break-all text-xs font-normal text-muted-foreground">{user.email}</span>}{opening === user.id && <span role="status" className="block text-xs font-normal">Opening…</span>}</span></Button>)}
    </div>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
  </div>
}
