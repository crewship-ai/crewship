"use client"

import type { ChatTurn } from "@/hooks/use-chat"
import { useChatAgent } from "./chat-agent-context"
import { ThinkingAvatar } from "./messages/thinking-avatar"

export interface StreamingIndicatorProps {
  isStreaming: boolean
  /** Stop was pressed and the run has not ended yet. */
  stopping?: boolean
  turns: Pick<ChatTurn, "role" | "parts" | "isStreaming">[]
  agentName?: string
}

/**
 * The one "agent is working" signal between send and the agent's first output.
 *
 * The agent's face with its ring is the transcript's only spinner (see
 * ThinkingAvatar). A streaming agent turn draws that face itself, so this row
 * exists only while no such turn is on screen: right after send, and after the
 * start notice replaced the "Starting…" status turn. It used to add typing
 * dots and a shimmer band on top of the face, which made two to four working
 * signals at once. After Stop it says so, without the ring.
 */
export function StreamingIndicator({ isStreaming, stopping, turns, agentName }: StreamingIndicatorProps) {
  const agent = useChatAgent()
  if (!isStreaming || turns.length === 0) return null
  const who = agentName?.trim() || "Agent"
  if (stopping) {
    return (
      <div role="status" className="flex items-center gap-3 px-4 py-3 text-label text-muted-foreground" data-slot="streaming-indicator">
        <ThinkingAvatar agent={agent} active={false} />
        <span>Stopping…</span>
      </div>
    )
  }
  const lastUserIndex = turns.findLastIndex((turn) => turn.role === "user")
  const faceOnScreen = turns.slice(lastUserIndex + 1).some((turn) => turn.role === "assistant" && turn.isStreaming)
  if (faceOnScreen) return null
  return (
    <div role="status" className="flex items-center gap-3 px-4 py-3 text-label text-muted-foreground animate-in fade-in duration-300 motion-reduce:animate-none" data-slot="streaming-indicator">
      <ThinkingAvatar agent={agent} active />
      <span>{`${who} is working…`}</span>
    </div>
  )
}
