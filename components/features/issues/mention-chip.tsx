"use client"

// The rendered form of an `@mention`. See lib/mentions.ts for the wire
// format and why it is shaped the way it is.
//
// The rule this file exists to enforce: **a chip's identity comes from the
// roster, never from the comment body.** The body supplies one thing — an
// agent id — and everything on screen (name, face) is looked up. A body that
// names an agent it is not addressing therefore renders the agent it *is*
// addressing, and a body addressing nobody real renders as text.

import * as React from "react"

import { cn } from "@/lib/utils"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import type { MentionAgent, MentionDirectory } from "@/lib/mentions"

const DirectoryContext = React.createContext<MentionDirectory | null>(null)

/**
 * Supplies the roster a mention resolves against. Absent, or missing the id,
 * a mention is not a mention — it is the text somebody typed.
 */
export function MentionDirectoryProvider({
  directory,
  children,
}: {
  directory: MentionDirectory | null | undefined
  children: React.ReactNode
}) {
  return <DirectoryContext.Provider value={directory ?? null}>{children}</DirectoryContext.Provider>
}

export function useMentionDirectory(): MentionDirectory | null {
  return React.useContext(DirectoryContext)
}

/** Avatar seed, in the order the rest of the app resolves one. */
export function mentionAvatarSeed(agent: MentionAgent): string {
  return agent.avatar_seed || agent.slug || agent.name || agent.id
}

/**
 * An agent, as a chip. Not interactive: a comment body is a reading surface,
 * and a focusable element per mention buys a tab stop nobody asked for.
 */
export function MentionChip({
  agent,
  className,
}: {
  agent: MentionAgent
  className?: string
}) {
  return (
    <span
      data-testid="mention-chip"
      data-agent-id={agent.id}
      title={agent.role_title ? `${agent.name} · ${agent.role_title}` : agent.name}
      className={cn(
        "inline-flex max-w-full items-center gap-1 rounded-full border border-primary/30 bg-primary/10",
        "py-0.5 pl-0.5 pr-1.5 align-baseline text-[0.9em] font-medium text-primary",
        className,
      )}
    >
      <AgentAvatar
        seed={mentionAvatarSeed(agent)}
        style={agent.avatar_style}
        avatarUrl={agent.avatar_url}
        className="h-[1.15em] w-[1.15em]"
        alt=""
      />
      <span className="truncate">@{agent.name}</span>
    </span>
  )
}

/**
 * A mention as stored: an id, plus the label its author wrote. Resolves to a
 * chip, or degrades to the label as plain text — the label is escaped by
 * React like any other string, and is never markup.
 */
export function ResolvedMention({ agentId, label }: { agentId: string; label: string }) {
  const directory = useMentionDirectory()
  const agent = directory?.get(agentId)
  if (!agent) {
    return <span className="text-muted-foreground">@{label || agentId}</span>
  }
  return <MentionChip agent={agent} />
}
