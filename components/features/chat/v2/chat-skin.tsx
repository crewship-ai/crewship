"use client"

import { createContext, useContext, useMemo, type ReactNode } from "react"

/**
 * Which presentation the transcript renders in.
 *
 * `/chat` is "classic" and stays byte-identical: the provider is absent
 * there, so `useChatSkin` returns the default and every `variant === "v2"`
 * branch is dead code on that route. `/chat2` wraps the same ChatPanel in
 * `variant="v2"` and gets the gutter layout, the avatar thinking indicator
 * and hover-only telemetry.
 *
 * A context rather than a prop: the presentation switch has to reach
 * TurnRenderer and AssistantTurn, which sit five levels under ChatPanel
 * behind a virtualiser. Threading a prop through would touch every one of
 * those signatures — and every test that mounts them — to carry a value
 * none of them act on.
 *
 * The agent travels with the skin because the transcript needs a face for
 * the left gutter and the turn itself carries no agent identity: a
 * `ChatTurn` has a role, parts and a timestamp, and the panel's own agent
 * is the only thing that can say whose reply it is.
 */

export interface ChatSkinAgent {
  id: string
  name: string
  /** DiceBear seed. Falls back to the agent id, matching every other caller. */
  avatarSeed?: string | null
  avatarStyle?: string | null
  /** The agent's stored render, when it has one. */
  avatarUrl?: string | null
}

export type ChatSkinVariant = "classic" | "v2"

interface ChatSkinValue {
  variant: ChatSkinVariant
  agent: ChatSkinAgent | null
}

const DEFAULT: ChatSkinValue = { variant: "classic", agent: null }

const ChatSkinContext = createContext<ChatSkinValue>(DEFAULT)

export function useChatSkin(): ChatSkinValue {
  return useContext(ChatSkinContext)
}

export function ChatSkinProvider({
  variant,
  agent,
  children,
}: {
  variant: ChatSkinVariant
  agent: ChatSkinAgent | null
  children: ReactNode
}) {
  const value = useMemo<ChatSkinValue>(() => ({ variant, agent }), [variant, agent])
  return <ChatSkinContext.Provider value={value}>{children}</ChatSkinContext.Provider>
}
