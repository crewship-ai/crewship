"use client"

import { createContext, useContext, useMemo, type ReactNode } from "react"

/**
 * Who the open conversation is with.
 *
 * A context rather than a prop because three things five levels below
 * ChatPanel need it — the transcript's gutter avatar, the reasoning header's
 * label, and the Files panel's "is this the agent's work or ours" test — and
 * none of them sits on a path that already carries an agent. Threading a prop
 * would touch every signature in between, and every test that mounts them, to
 * carry a value they do not act on.
 *
 * This started as a SKIN: a `variant` of "classic" | "v2" that let the old
 * chat surface and the new one render the same turn two ways while they were
 * judged side by side. The new one won, the old one is deleted, and the
 * variant went with it — a switch with one arm is not a switch, and the branch
 * that never runs is the branch that rots.
 */

export interface ChatAgent {
  id: string
  name: string
  /** Canonical slug. With crewId it is what turns a storage key into a path
   *  relative to the agent, which is the only form the file classifier can
   *  read (see files/file-scope.ts). */
  slug?: string | null
  crewId?: string | null
  /** DiceBear seed. Falls back to the agent id, matching every other caller. */
  avatarSeed?: string | null
  avatarStyle?: string | null
  /** The agent's stored render, when it has one. */
  avatarUrl?: string | null
}

const ChatAgentContext = createContext<ChatAgent | null>(null)

/**
 * The open conversation's agent, or null.
 *
 * Null is a real answer, not a failure: ChatPanel is mounted in tests and in
 * the odd embedded surface without a provider, and every consumer degrades to
 * something sensible rather than throwing.
 */
export function useChatAgent(): ChatAgent | null {
  return useContext(ChatAgentContext)
}

export function ChatAgentProvider({
  agent,
  children,
}: {
  agent: ChatAgent | null
  children: ReactNode
}) {
  // Memoised on the fields rather than on the object: the page rebuilds this
  // record whenever the roster refetches, and an identity change would
  // re-render the whole transcript for a value that did not move.
  const value = useMemo(
    () => agent,
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      agent?.id,
      agent?.name,
      agent?.slug,
      agent?.crewId,
      agent?.avatarSeed,
      agent?.avatarStyle,
      agent?.avatarUrl,
    ],
  )
  return <ChatAgentContext.Provider value={value}>{children}</ChatAgentContext.Provider>
}
