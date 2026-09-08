"use client"

import { createContext, useCallback, useContext, useEffect, useState } from "react"
import { useSearchParams } from "next/navigation"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Bot, ChevronDown, Hash, MessagesSquare, MessageSquare, Plus, Users } from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSessionSafe } from "@/hooks/use-auth"
import { useWorkspaceConversations, conversationRequest, type WorkspaceConversation } from "@/hooks/use-workspace-conversations"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { CreateConversation, ConversationThread } from "./workspace-conversations"
import { ConversationIcon, conversationTitle } from "./conversation-identity"
import { DirectMessagePicker } from "./direct-message-picker"
import { cn } from "@/lib/utils"

type Facet = "all" | "agents" | "people" | "rooms"
type CreateKind = "person" | "group" | "channel" | null
interface UnifiedChatContextValue {
  selectedId: string | null
  selectionVersion: number
  select: (id: string | null) => void
  clearRoom: () => void
  facet: Facet
  setFacet: (facet: Facet) => void
  create: (kind: CreateKind) => void
  workspaceId: string
  userId: string
  list: ReturnType<typeof useWorkspaceConversations>["list"]
  refresh: () => void
}
const UnifiedChatContext = createContext<UnifiedChatContextValue | null>(null)
export const useUnifiedConversations = () => useContext(UnifiedChatContext)

export function UnifiedChatProvider({ children }: { children: React.ReactNode }) {
  const { workspaceId } = useWorkspace()
  const { data: session } = useSessionSafe()
  if (!workspaceId || !session?.user.id) return <>{children}</>
  return <WorkspaceChatProvider key={`${workspaceId}:${session.user.id}`} workspaceId={workspaceId} userId={session.user.id}>{children}</WorkspaceChatProvider>
}
function WorkspaceChatProvider({ workspaceId, userId, children }: { workspaceId: string; userId: string; children: React.ReactNode }) {
  const params = useSearchParams()
  const [selectedId, setSelectedId] = useState<string | null>(params.get("conversation"))
  const [facet, setFacet] = useState<Facet>("all")
  const [selectionVersion, setSelectionVersion] = useState(0)
  const [creating, setCreating] = useState<CreateKind>(null)
  const { list, refresh } = useWorkspaceConversations(workspaceId, userId)
  const queryClient = useQueryClient()
  useEffect(() => { setSelectedId(params.get("conversation")) }, [params])
  useEffect(() => {
    const read = () => setSelectedId(new URLSearchParams(window.location.search).get("conversation"))
    window.addEventListener("popstate", read)
    return () => window.removeEventListener("popstate", read)
  }, [])
  function select(id: string | null) {
    setSelectedId(id)
    setSelectionVersion((value) => value + 1)
    const url = new URL(window.location.href)
    url.pathname = "/chat"
    for (const key of ["agent", "session", "prompt", "kind"]) url.searchParams.delete(key)
    if (id) url.searchParams.set("conversation", id)
    else url.searchParams.delete("conversation")
    if (`${window.location.pathname}${window.location.search}` !== `${url.pathname}${url.search}`) window.history.pushState(null, "", url)
    else window.history.replaceState(null, "", url)
  }
  function opened(conversation: WorkspaceConversation) {
    queryClient.setQueryData(["workspace-conversations", workspaceId, userId, conversation.id, "detail"], conversation)
    setFacet("all")
    select(conversation.id)
    setCreating(null)
    refresh()
  }
  const clearRoom = useCallback(() => setSelectedId(null), [])
  const value: UnifiedChatContextValue = { selectedId, selectionVersion, select, clearRoom, facet, setFacet, create: setCreating, workspaceId, userId, list, refresh }
  return <UnifiedChatContext.Provider value={value}>
    {children}
    <Dialog open={creating !== null} onOpenChange={(open) => { if (!open) setCreating(null) }}><DialogContent><DialogHeader><DialogTitle>{creating === "person" ? "New direct message" : creating === "channel" ? "New workspace channel" : "New group"}</DialogTitle><DialogDescription>{creating === "person" ? "Choose a colleague. Your existing direct conversation opens automatically; only the two of you can access it." : creating === "channel" ? "Everyone in this workspace can read and write. You can add agents with explicit access to channel history." : "Choose the people who can access this group and its history."}</DialogDescription></DialogHeader>{creating === "person" ? <DirectMessagePicker workspaceId={workspaceId} userId={userId} onOpened={opened} /> : creating && <CreateConversation key={creating} workspaceId={workspaceId} userId={userId} initialKind={creating} onCreated={opened} />}</DialogContent></Dialog>
  </UnifiedChatContext.Provider>
}

export function UnifiedNewChatMenu({ onAgent }: { onAgent: () => void }) {
  const chat = useUnifiedConversations()
  if (!chat) return null
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" className="h-8 w-full justify-start gap-2 rounded-md bg-transparent text-xs font-medium shadow-none">
          <Plus className="size-4 text-muted-foreground" />New chat
          <ChevronDown className="ml-auto size-3.5 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72 max-w-[calc(100vw-24px)]">
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Direct messages</DropdownMenuLabel>
        <DropdownMenuItem className="items-start gap-3 py-2" onSelect={() => { chat.setFacet("agents"); onAgent() }}>
          <Bot className="mt-0.5 size-4" /><span><span className="block">Chat with an agent</span><span className="block text-xs text-muted-foreground">Start a new agent session</span></span>
        </DropdownMenuItem>
        <DropdownMenuItem className="items-start gap-3 py-2" onSelect={() => chat.create("person")}>
          <MessageSquare className="mt-0.5 size-4" /><span><span className="block">Message a person</span><span className="block text-xs text-muted-foreground">Open a direct chat with a colleague</span></span>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Shared conversations</DropdownMenuLabel>
        <DropdownMenuItem className="items-start gap-3 py-2" onSelect={() => chat.create("group")}>
          <Users className="mt-0.5 size-4" /><span><span className="block">Create a group</span><span className="block text-xs text-muted-foreground">For selected participants</span></span>
        </DropdownMenuItem>
        <DropdownMenuItem className="items-start gap-3 py-2" onSelect={() => chat.create("channel")}>
          <Hash className="mt-0.5 size-4" /><span><span className="block">Create a channel</span><span className="block text-xs text-muted-foreground">For everyone in this workspace</span></span>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
export function UnifiedChatFacets() {
  const chat = useUnifiedConversations()
  if (!chat) return null
  return <div className="border-b px-2 py-2"><h2 className="mb-2 px-1 text-sm font-semibold">Chat</h2><div aria-label="Conversation types" className="grid grid-cols-2 gap-1">{([["all", "All", MessagesSquare], ["agents", "Agents", Bot], ["people", "People", Users], ["rooms", "Groups & channels", Hash]] as const).map(([id, label, Icon]) => <button type="button" key={id} aria-pressed={chat.facet === id} onClick={() => chat.setFacet(id)} className={cn("flex min-h-9 items-center gap-2 rounded-md px-2 text-xs text-left hover:bg-accent", chat.facet === id && "bg-accent text-foreground")}><Icon aria-hidden="true" className="size-3.5 shrink-0" />{label}</button>)}</div></div>
}
export function UnifiedConversationSection({ query, onSelect, section }: { query: string; onSelect?: () => void; section?: "people" | "rooms" }) {
  const chat = useUnifiedConversations()
  if (!chat || (!section && chat.facet === "agents")) return null
  const rows = chat.list.data?.pages.flatMap((page) => page.conversations).filter((room) => room.title.toLowerCase().includes(query.toLowerCase()) && (section ? section === "people" ? room.is_direct : !room.is_direct : chat.facet === "all" || (chat.facet === "people" ? room.is_direct : !room.is_direct)))
  return <section aria-label="People and shared conversations" className={section ? "pb-1" : "border-b pb-2"}>{!section && <h3 className="px-3 py-2 text-xs text-muted-foreground">People, groups &amp; channels</h3>}{chat.list.isPending && <p className="px-3 py-2 text-xs">Loading conversations…</p>}{chat.list.error && <div role="alert" className="p-3 text-xs">Unable to load shared conversations.<Button type="button" variant="ghost" onClick={() => { void chat.list.refetch() }}>Retry</Button></div>}{!chat.list.error && rows?.map((room) => <button key={room.id} type="button" onClick={() => { chat.select(room.id); onSelect?.() }} aria-current={chat.selectedId === room.id ? "page" : undefined} className={cn("flex min-h-12 w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm hover:bg-accent", chat.selectedId === room.id && "bg-accent")}>
    <ConversationIcon conversation={room} className="size-8" /><span className="min-w-0 flex-1"><span className="block truncate">{conversationTitle(room)}</span><span className="block text-xs text-muted-foreground">{room.is_direct ? "Direct message" : room.kind === "channel" ? "Workspace channel" : "Group"}</span></span>{room.unread_count > 0 && <span className="rounded-full bg-primary/15 px-2 text-xs text-primary">{room.unread_count}</span>}
  </button>)}{!chat.list.isPending && !chat.list.error && rows?.length === 0 && <p className="px-3 py-2 text-xs text-muted-foreground">{query ? "No matching conversations." : section === "people" ? "No direct messages yet." : section === "rooms" ? "No groups or channels yet." : "Use New chat to start with a person or a group."}</p>}{chat.list.hasNextPage && <Button type="button" variant="ghost" disabled={chat.list.isFetchingNextPage} onClick={() => { void chat.list.fetchNextPage() }}>Load more conversations</Button>}</section>
}
export function UnifiedConversationPanel({ onBack }: { onBack: () => void }) {
  const queryClient = useQueryClient()
  const chat = useUnifiedConversations()!
  const detail = useQuery({ queryKey: ["workspace-conversations", chat.workspaceId, chat.userId, chat.selectedId, "detail"], enabled: !!chat.selectedId, queryFn: ({ signal }) => conversationRequest<WorkspaceConversation>(chat.workspaceId, `conversations/${encodeURIComponent(chat.selectedId!)}`, undefined, signal) })
  if (detail.isPending) return <p className="p-6 text-sm">Loading conversation…</p>
  if (detail.error || !detail.data) return <div role="alert" className="space-y-3 p-6 text-sm"><p>This conversation is unavailable or you no longer have access.</p><Button type="button" variant="outline" onClick={() => { void detail.refetch() }}>Retry</Button><Button type="button" variant="ghost" onClick={onBack}>Back to conversations</Button></div>
  return <div className="flex h-full min-h-0 flex-col"><ConversationThread key={`${chat.workspaceId}:${chat.userId}:${detail.data.id}`} conversation={detail.data} workspaceId={chat.workspaceId} userId={chat.userId} refresh={chat.refresh} onBack={onBack} onOpenConversation={(conversation) => { queryClient.setQueryData(["workspace-conversations", chat.workspaceId, chat.userId, conversation.id, "detail"], conversation); chat.refresh(); chat.select(conversation.id) }} /></div>
}
