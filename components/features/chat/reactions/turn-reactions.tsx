"use client"

import { useEffect } from "react"

import { useReactionsStore } from "@/stores/reactions-store"
import { ReactionPicker } from "./reaction-picker"
import { ReactionsRow } from "./reactions-row"

/** Props shared by the row and the picker: a reaction is addressed by
 *  (chat, message), which is exactly what the API path wants. `chatId`
 *  is optional only because AssistantTurn's is — see below. */
interface TurnReactionsProps {
  chatId?: string
  messageId: string
}

/**
 * The reactions row under one assistant turn.
 *
 * Hydrates from the server on mount, which is what makes a teammate's
 * reaction visible at all: nothing else in the chat payload carries
 * reactions, so this is one GET per rendered turn. That is the cost of
 * the endpoint's shape (there is no bulk read); if it starts to matter,
 * the fix is to fold reactions into the history response, not to cache
 * them client-side again.
 *
 * Renders nothing without a chat id — a reaction that cannot name its
 * chat cannot be stored, and a local-only tally is the bug this
 * replaced.
 */
export function TurnReactions({
  chatId,
  messageId,
  streaming,
}: TurnReactionsProps & { streaming: boolean }) {
  const reactions = useReactionsStore((s) => s.byTurn[messageId])
  const hydrate = useReactionsStore((s) => s.hydrate)
  const toggle = useReactionsStore((s) => s.toggle)

  // A streaming turn has no persisted message yet, so there is nothing
  // to read; the effect re-runs when it settles.
  useEffect(() => {
    if (!chatId || streaming) return
    void hydrate(chatId, messageId)
  }, [chatId, messageId, streaming, hydrate])

  if (streaming || !chatId) return null
  if (!reactions || Object.keys(reactions).length === 0) return null

  return (
    <ReactionsRow
      reactions={reactions}
      onToggle={(emoji) => void toggle(chatId, messageId, emoji)}
      className="mt-1"
    />
  )
}

/** The emoji picker in the turn's action bar, bound to the same
 *  (chat, message) the row reads. */
export function TurnReactionPicker({ chatId, messageId }: TurnReactionsProps) {
  const add = useReactionsStore((s) => s.add)
  if (!chatId) return null
  return <ReactionPicker onPick={(emoji) => void add(chatId, messageId, emoji)} />
}
