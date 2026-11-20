import type { ChatTurn } from "@/hooks/use-chat"

/**
 * React keys for the transcript.
 *
 * A sent message is drawn at once with a client id, and the history reload
 * after the run swaps it for the stored message, which has the server's id.
 * Keyed by id, that swap unmounted one bubble and mounted another, and the
 * list's exit/enter animation (AnimatePresence, popLayout) showed both on top
 * of each other for a moment. A user turn is keyed by its place among the
 * user turns and its text instead, which the swap does not change; an edit
 * changes the text and so still gets a new bubble. Other turns keep their id.
 */
export function turnRenderKeys(turns: Pick<ChatTurn, "id" | "role" | "parts">[]): string[] {
  let ordinal = 0
  return turns.map((turn) => {
    if (turn.role !== "user") return turn.id
    const text = turn.parts.filter((p) => p.type === "text").map((p) => p.content).join("\n").trim()
    return `user:${ordinal++}:${text}`
  })
}
