import type { ChatTurn } from "@/hooks/use-chat"

const TURN_END: Partial<Record<NonNullable<ChatTurn["turnState"]>, [partial: string, empty: string]>> = {
  cancelled: ["Stopped. The reply above is partial.", "Stopped before any reply."],
  interrupted: ["Interrupted. The reply above is partial.", "Interrupted before any reply."],
  failed: ["The run failed. The reply above may be incomplete.", "The run failed before any reply."],
}

/** How a finished turn that did not complete ended, in the words the chat shows under it. */
export function turnEndText(turn: ChatTurn, empty: boolean): string | undefined {
  if (turn.isStreaming || !turn.turnState) return undefined
  return TURN_END[turn.turnState]?.[empty ? 1 : 0]
}
