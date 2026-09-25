"use client"

import { useEffect, useRef, type ReactNode } from "react"
import { X } from "lucide-react"
import { motion, useReducedMotion } from "motion/react"
import { Button } from "@/components/ui/button"
import type { WorkspaceConversation } from "@/hooks/use-workspace-conversations"
import { conversationTitle } from "./conversation-identity"

/** Shares the transcript's layout; never places a modal/backdrop over the workspace. */
export function ConversationDetailsPanel({ section, conversation, canManage, onClose, general, members, agents, activity }: {
  section: "general" | "members"
  conversation: WorkspaceConversation
  canManage: boolean
  onClose: () => void
  general: ReactNode
  members: ReactNode
  agents?: ReactNode
  activity?: ReactNode
}) {
  const generalHeading = useRef<HTMLHeadingElement>(null)
  const membersHeading = useRef<HTMLHeadingElement>(null)
  const reducedMotion = useReducedMotion()
  const title = conversation.is_direct ? "Direct message participants" : conversation.kind === "channel" ? canManage ? "Channel settings" : "Channel details" : canManage ? "Group settings" : "Group details"
  useEffect(() => {
    const target = section === "members" ? membersHeading.current : generalHeading.current
    target?.focus({ preventScroll: true })
    target?.scrollIntoView({ block: "nearest" })
  }, [section])

  return <motion.aside aria-label={title} initial={{ x: reducedMotion ? 0 : 24, opacity: 0 }} animate={{ x: 0, opacity: 1 }} transition={{ duration: reducedMotion ? 0 : 0.18 }}
    onKeyDown={(event) => { if (event.key === "Escape" && !event.defaultPrevented) { event.stopPropagation(); onClose() } }}
    className="absolute inset-0 z-20 flex min-h-0 flex-col border-l bg-card @min-[720px]:static @min-[720px]:w-[360px] @min-[720px]:shrink-0">
    <div className="flex min-h-0 flex-1 flex-col bg-accent/30">
      <header className="flex shrink-0 items-start gap-2 border-b px-3 py-3">
        <div className="min-w-0 flex-1"><h2 className="text-sm font-semibold">{title}</h2><p className="mt-0.5 truncate text-xs text-muted-foreground">{conversationTitle(conversation)}</p></div>
        <Button type="button" variant="ghost" size="icon" className="size-7 coarse:size-12" aria-label="Close conversation details" onClick={onClose}><X className="size-4" /></Button>
      </header>
      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-3 text-xs">
        <section aria-label="General"><h3 ref={generalHeading} tabIndex={-1} className="mb-3 text-[10px] uppercase tracking-wider text-muted-foreground outline-none">General</h3>{general}</section>
        <section aria-label="Members" className="space-y-3 border-t pt-4"><h3 ref={membersHeading} tabIndex={-1} className="text-[10px] uppercase tracking-wider text-muted-foreground outline-none">Members</h3>{members}</section>
        {agents}
        {activity}
      </div>
    </div>
  </motion.aside>
}
