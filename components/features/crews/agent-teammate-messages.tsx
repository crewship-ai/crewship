"use client"

import { formatRelativeTime } from "@/lib/time"

import { CrCard, CrLine, CrNote } from "./cr-card"
import type { PeerMessageRow } from "./agent-canvas-tabs/types"

const SHOWN = 5

/** One peer exchange as a row: who, the question, and where it stands. */
export function teammateLine(m: PeerMessageRow): { title: string; state: string } {
  const incoming = m.direction !== "outgoing"
  const who = incoming
    ? m.from_agent_name ?? m.from_agent_slug ?? "A teammate"
    : `To ${m.to_agent_name ?? m.to_agent_slug ?? "a teammate"}`
  const question = (m.question ?? m.preview ?? "").trim()
  const state = m.status === "FAILED" ? "failed" : m.response || m.status === "COMPLETED" ? "answered" : "waiting for an answer"
  return { title: question ? `${who}: “${question}”` : who, state }
}

/**
 * A-OV "Messages from teammates": the agent's peer questions in both
 * directions, from the agent's inbox summary (the 20 newest in its crew).
 */
export function TeammateMessages({ messages, unavailable = false }: { messages: PeerMessageRow[]; unavailable?: boolean }) {
  return (
    <CrCard title="Messages from teammates" concept="chat" count={messages.length || undefined} data-testid="agent-teammate-messages">
      {unavailable && messages.length === 0 ? (
        <CrNote tone="error">Messages from teammates could not be loaded.</CrNote>
      ) : messages.length === 0 ? (
        <CrNote>No messages from teammates yet.</CrNote>
      ) : (
        messages.slice(0, SHOWN).map((m, i) => {
          const { title, state } = teammateLine(m)
          return (
            <CrLine
              key={m.id ?? i}
              title={title}
              sub={["Peer question", m.created_at ? formatRelativeTime(m.created_at) : null, state].filter(Boolean).join(" · ")}
            />
          )
        })
      )}
    </CrCard>
  )
}
