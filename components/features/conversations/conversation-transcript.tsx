"use client"

import { Activity, AtSign } from "lucide-react"
import { MessageResponse } from "@/components/ai-elements/message"
import type { WorkspaceMessage } from "@/hooks/use-workspace-conversations"
import { ConversationLink } from "./conversation-link"
import { ConversationIdentity } from "./conversation-identity"

const markdownComponents = { a: ConversationLink }

export function sameMessageGroup(previous: WorkspaceMessage | undefined, message: WorkspaceMessage): boolean {
  if (!previous || (!previous.author_user_id && !previous.author_agent_id) || (!message.author_user_id && !message.author_agent_id) || previous.kind === "system" || message.kind === "system" || previous.kind === "agent_joined" || previous.kind === "agent_left" || message.kind === "agent_joined" || message.kind === "agent_left") return false
  const before = new Date(previous.created_at), current = new Date(message.created_at)
  const delta = current.getTime() - before.getTime()
  return previous.author_user_id === message.author_user_id && previous.author_agent_id === message.author_agent_id && delta >= 0 && delta < 5 * 60_000 && before.toDateString() === current.toDateString()
}

export function ConversationTranscript({ history, userId, agentNames }: { history: WorkspaceMessage[]; userId: string; agentNames: Map<string, string> }) {
  return <>{history.map((message, index) => {
    const previous = history[index - 1]
    const date = new Date(message.created_at)
    const validDate = Number.isFinite(date.getTime())
    const newDay = !previous || new Date(previous.created_at).toDateString() !== date.toDateString()
    const grouped = sameMessageGroup(previous, message)
    const system = message.source_kind === "activity" || message.kind === "agent_joined" || message.kind === "agent_left"
    const name = system && !message.author_agent_id && !message.author_user_id ? "Crewship" : message.author_name || (message.author_agent_id ? "Agent" : message.author_user_id ? "Workspace member" : "Former participant")
    const time = validDate ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : ""
    return <div key={message.id}>
      {newDay && validDate && <div className="my-5 flex items-center gap-3" role="separator" aria-label={date.toLocaleDateString()}><div className="h-px flex-1 bg-border" /><span className="rounded-full border bg-background px-3 py-1 text-xs font-medium text-muted-foreground">{date.toLocaleDateString([], { weekday: "long", month: "short", day: "numeric", year: "numeric" })}</span><div className="h-px flex-1 bg-border" /></div>}
      <article aria-label={`${name}${time ? ` at ${time}` : ""}`} className={`group flex gap-3 rounded-lg px-2 hover:bg-muted/30 ${grouped ? "py-0.5" : "pb-1 pt-3"}`}>
        <div className="w-9 shrink-0">{!grouped && (system ? <span aria-hidden="true" className="flex size-9 items-center justify-center rounded-lg border bg-muted/50 text-muted-foreground"><Activity className="size-4" /></span> : <ConversationIdentity id={message.author_agent_id || message.author_user_id || message.id} name={name} avatarUrl={message.author_avatar_url} avatarStyle={message.author_avatar_style} slug={message.author_avatar_seed || message.author_slug} agent={!!message.author_agent_id} />)}{grouped && <time dateTime={message.created_at} title={validDate ? date.toLocaleString() : undefined} className="block pt-1 text-[10px] text-muted-foreground opacity-0 group-hover:opacity-100 group-focus-within:opacity-100">{time}</time>}</div>
        <div className="min-w-0 flex-1">{!grouped && <div className="mb-1 flex flex-wrap items-baseline gap-2 text-sm"><span className="font-semibold">{name}{!message.author_agent_id && message.author_user_id === userId ? " (you)" : ""}</span>{message.author_agent_id && <span className="rounded border bg-muted px-1 text-[10px] font-medium text-muted-foreground">Agent</span>}{system && <span className="text-xs text-muted-foreground">Activity</span>}<time dateTime={message.created_at} title={validDate ? date.toLocaleString() : undefined} className="text-xs text-muted-foreground">{time}</time></div>}
          <MessageResponse components={markdownComponents} mode="static" className="h-auto min-w-0 text-sm leading-relaxed [overflow-wrap:anywhere] [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_table]:text-xs">{message.content}</MessageResponse>
          {!!message.mentioned_agent_ids?.length && <p className="mt-1 flex items-center gap-1 text-xs text-muted-foreground"><AtSign aria-hidden="true" className="size-3" />Asked: {message.mentioned_agent_ids.map((id) => agentNames.get(id) || "Agent").join(", ")}</p>}
        </div>
      </article>
    </div>
  })}</>
}
