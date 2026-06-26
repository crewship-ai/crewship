"use client"

import React from "react"
import { motion } from "motion/react"
import {
  AlertCircle,
  Settings2,
  Wrench,
  RefreshCw,
} from "lucide-react"
import {
  Message,
  MessageContent,
} from "@/components/ai-elements/message"
import { arrival } from "@/lib/motion"
import type { ChatTurn } from "@/hooks/use-chat"
import { AssistantTurn } from "./assistant-turn"
import { EditableUserMessage } from "./messages/editable-user-message"
import { CrewProvisioningCard } from "./crew-provisioning-card"

function formatTimestamp(date: Date): string {
  return date.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
}

interface TurnRendererProps {
  turn: ChatTurn
  onCopy: (s: string) => void
  onFileClick: (s: string) => void
  isLastAssistant?: boolean
  onRegenerate?: () => void
  onEditUserMessage?: (turnId: string, newContent: string) => void
  /** Epoch ms cutoff. Turns whose timestamp is BEFORE this skip the
   *  arrival animation — they're either loaded from history or already
   *  rendered before the user switched session. */
  animateAfter?: number
  /** Active agent — forwarded to AssistantTurn so artifact tabs are
   *  scoped to the agent that produced the turn. Optional; tests and
   *  legacy callers can omit it (the artifact affordance hides itself). */
  agentId?: string
  /** Active chat id — forwarded to AssistantTurn so feedback rows
   *  land in the right workspace via the chats.workspace_id derivation
   *  on the server. Without this, the API falls back to the user's
   *  primary workspace, which on a multi-workspace user attaches the
   *  row to the wrong tenant. */
  chatId?: string
  /** Resolves a user id to a display name for group-chat author attribution.
   *  Returns null for the local user (no label) or when no participant info is
   *  available. Optional — callers without group context omit it. */
  resolveAuthorName?: (userId: string) => string | null
}

/** Render a single turn (user, assistant, or system). */
export const TurnRenderer = React.memo(function TurnRenderer({ turn, onCopy, onFileClick, isLastAssistant, onRegenerate, onEditUserMessage, animateAfter, agentId, chatId, resolveAuthorName }: TurnRendererProps) {
  const shouldAnimate = animateAfter == null || turn.timestamp.getTime() >= animateAfter
  const initialAnim = shouldAnimate ? arrival.initial : false
  const transition = shouldAnimate ? arrival.transition : { duration: 0 }
  if (turn.role === "user") {
    const textContent = turn.parts.find((p) => p.type === "text")?.content ?? ""
    // Group-chat attribution: a teammate's message shows their name; the local
    // user's own turns (resolver returns null) render as today.
    const authorName = turn.authorUserId ? resolveAuthorName?.(turn.authorUserId) ?? null : null
    return (
      <motion.div
        initial={initialAnim}
        animate={arrival.animate}
        exit={arrival.exit}
        transition={transition}
        data-turn-id={turn.id}
        className="group flex flex-col"
      >
        {authorName && (
          <div className="ml-auto mb-0.5 flex items-center gap-1.5 text-micro text-muted-foreground">
            <span className="inline-flex h-4 w-4 items-center justify-center rounded-full bg-primary/15 text-[9px] font-semibold text-primary">
              {authorName.charAt(0).toUpperCase()}
            </span>
            <span>{authorName}</span>
          </div>
        )}
        {onEditUserMessage && !authorName ? (
          <EditableUserMessage
            text={textContent}
            timestamp={turn.timestamp}
            onSave={(next) => onEditUserMessage(turn.id, next)}
          />
        ) : (
          <Message from="user">
            <MessageContent>
              <div className="flex items-start gap-2">
                <span>{textContent}</span>
              </div>
            </MessageContent>
            <div className="text-micro text-muted-foreground ml-auto opacity-0 group-hover:opacity-100 transition-opacity">
              {formatTimestamp(turn.timestamp)}
            </div>
          </Message>
        )}
      </motion.div>
    )
  }

  if (turn.role === "system") {
    const part = turn.parts[0]
    const content = part?.content ?? ""
    const isError = part?.type === "error"
    const isInit = part?.type === "system_init"
    const isCrewProvisioning = part?.type === "crew_provisioning"

    if (isCrewProvisioning) {
      const meta = part?.metadata ?? {}
      const crewId = meta.crew_id as string | undefined
      const crewSlug = meta.crew_slug as string | undefined
      const enqueueStatus = meta.status as string | undefined
      const enqueueError = meta.error as string | undefined
      return (
        <motion.div
          initial={initialAnim}
          animate={arrival.animate}
          transition={transition}
          data-turn-id={turn.id}
          className="px-4 py-2"
        >
          <CrewProvisioningCard
            crewId={crewId}
            crewSlug={crewSlug}
            message={content}
            enqueueStatus={enqueueStatus}
            enqueueError={enqueueError}
          />
        </motion.div>
      )
    }

    if (isInit) {
      const meta = part?.metadata ?? {}
      const model = meta.model as string | undefined
      const tools = meta.tools as string[] | undefined
      return (
        <motion.div
          initial={{ opacity: 0, scale: 0.96 }}
          animate={{ opacity: 1, scale: 1 }}
          transition={arrival.transition}
          className="flex items-center justify-center py-2"
        >
          <div className="flex items-center gap-3 px-4 py-2 bg-muted/40 border rounded-full text-label text-muted-foreground">
            <Settings2 className="h-3 w-3" />
            <span>Session started</span>
            {model && (
              <span className="font-mono text-micro bg-background px-1.5 py-0.5 rounded border">{model}</span>
            )}
            {tools && tools.length > 0 && (
              <span className="flex items-center gap-1">
                <Wrench className="h-3 w-3" />
                {tools.length} tools
              </span>
            )}
          </div>
        </motion.div>
      )
    }

    return (
      <motion.div
        initial={initialAnim}
        animate={arrival.animate}
        transition={transition}
        data-turn-id={turn.id}
      >
      <Message from="assistant">
        <MessageContent className={isError ? "border-destructive/50 bg-destructive/5 rounded-lg px-4 py-3" : ""}>
          <div className={`flex items-center gap-2 text-body ${isError ? "text-destructive" : "text-muted-foreground"}`}>
            <AlertCircle className="h-4 w-4 shrink-0" />
            {content}
          </div>
        </MessageContent>
      </Message>
      </motion.div>
    )
  }

  // Assistant turn - use the new grouped component
  return (
    <motion.div
      initial={initialAnim}
      animate={arrival.animate}
      transition={transition}
      data-turn-id={turn.id}
    >
      <AssistantTurn turn={turn} onCopy={onCopy} onFileClick={onFileClick} agentId={agentId} chatId={chatId} />
      {isLastAssistant && onRegenerate && !turn.isStreaming && (
        <div className="flex pl-4 -mt-1 mb-2">
          <button
            onClick={onRegenerate}
            className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
            title="Regenerate response"
          >
            <RefreshCw className="h-3 w-3" />
            <span>Regenerate</span>
          </button>
        </div>
      )}
    </motion.div>
  )
})
