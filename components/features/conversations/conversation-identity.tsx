"use client"

import { Hash, LockKeyhole } from "lucide-react"
import { UserAvatar } from "@/components/ui/user-avatar"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { cn } from "@/lib/utils"
import type { WorkspaceConversation } from "@/hooks/use-workspace-conversations"

const colors = ["bg-primary/15 text-primary", "bg-purple/15 text-purple", "bg-notice/15 text-notice", "bg-destructive/15 text-destructive", "bg-warn/15 text-warn", "bg-info/15 text-info"]
export function identityColor(id: string) {
  let hash = 0
  for (const letter of id) hash = (Math.imul(hash, 31) + letter.codePointAt(0)!) | 0
  return colors[(hash >>> 0) % colors.length]
}

/** No presence dot: a portrait does not imply somebody is online. */
export function ConversationIdentity({ id, name, avatarUrl, agent, slug, avatarStyle, className }: { id: string; name: string; avatarUrl?: string | null; agent?: boolean; slug?: string; avatarStyle?: string | null; className?: string }) {
  return <span aria-hidden="true" className="shrink-0">{agent
    ? <AgentAvatar seed={slug || id} style={avatarStyle} avatarUrl={avatarUrl} className={cn("size-9 rounded-lg", className)} />
    : <UserAvatar key={`${id}:${avatarUrl || ""}`} name={name} email={name} src={avatarUrl} className={cn("size-9 rounded-lg", identityColor(id), className)} textClassName="text-xs" />}</span>
}

export function conversationTitle(conversation: WorkspaceConversation): string {
  return conversation.is_direct ? conversation.direct_user_name || conversation.title : conversation.title
}

export function ConversationIcon({ conversation, className }: { conversation: WorkspaceConversation; className?: string }) {
  if (conversation.is_direct) return <ConversationIdentity id={conversation.direct_user_id || conversation.id} name={conversationTitle(conversation)} avatarUrl={conversation.direct_avatar_url} className={className} />
  const Icon = conversation.kind === "channel" ? Hash : LockKeyhole
  return <span aria-hidden="true" className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg border bg-muted/60 text-muted-foreground", className)}><Icon className="size-4" /></span>
}
