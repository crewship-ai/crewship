import type { ChatTurn } from "@/hooks/use-chat"
import { visibleAgentText } from "@/lib/chat-text"

const TURN_END: Partial<Record<NonNullable<ChatTurn["turnState"]>, [partial: string, empty: string]>> = {
  cancelled: ["Stopped. The reply above is partial.", "Stopped before any reply."],
  interrupted: ["Interrupted. The reply above is partial.", "Interrupted before any reply."],
  failed: ["The run failed. The reply above may be incomplete.", "The run failed before any reply."],
}

/** Describe the current local turn, without inferring a persisted server outcome. */
export function chatTurnStatusNote(turn: Pick<ChatTurn, "turnState" | "isStreaming" | "cancelRequested" | "parts">): string | undefined {
  if (turn.isStreaming) return turn.cancelRequested ? "Stopping…" : undefined
  const ended = turn.turnState ? TURN_END[turn.turnState] : undefined
  const hasVisibleParts = turn.parts.some(part => part.type !== "text" || visibleAgentText(part.content) !== "")
  return ended?.[hasVisibleParts ? 0 : 1]
}
