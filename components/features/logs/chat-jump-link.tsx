"use client"

import Link from "next/link"
import { MessageSquare } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"

// #2229 — chat.user_message used to render the first 240 characters of the
// message as its summary. It no longer carries the text at all: the row is a
// measurement ("user → morgan: 312 characters") plus a reference, because the
// journal is hash-chained, append-only and skipped by the GDPR erasure cascade,
// while the chat holding the real message is erasable.
//
// That trade is only honest if the reference is reachable. Before this, nothing
// in any journal surface turned chat_id into a link, so removing the preview
// would have left the row a dead end. This is the other half of the change.
//
// It renders for any entry carrying both halves — chat.agent_response gets it
// too, which is the same journey and was equally unreachable.

/**
 * Build the chat deep link for an entry, or null when it is not a chat entry.
 *
 * chat_id is read from the payload first and refs second: the emit site writes
 * it to both, but refs is the field the entry contract guarantees, so the
 * fallback is what keeps this working if the payload shape moves again.
 *
 * The URL shape mirrors `lib/conversation-search.ts` and
 * `internal/chatnotify/notify.go` — one spelling of "where a chat lives".
 * Both segments go through encodeURIComponent, which is also what stops a
 * payload-supplied slug steering navigation off-site: "//evil.example.com"
 * encodes to "%2F%2Fevil.example.com" and stays under /chat/.
 */
export function chatHrefForEntry(entry: JournalEntry): string | null {
  const payload = (entry.payload ?? {}) as Record<string, unknown>
  const refs = (entry.refs ?? {}) as Record<string, unknown>

  const slug = payload.agent_slug
  const chatId = payload.chat_id ?? refs.chat_id

  if (typeof slug !== "string" || slug === "") return null
  if (typeof chatId !== "string" || chatId === "") return null

  return `/chat/${encodeURIComponent(slug)}?session=${encodeURIComponent(chatId)}`
}

export function ChatJumpLink({ entry }: { entry: JournalEntry }) {
  const href = chatHrefForEntry(entry)
  if (!href) return null
  return (
    <Link
      href={href}
      onClick={(e) => e.stopPropagation()}
      className="inline-flex items-center gap-1 rounded border border-border/60 bg-card px-1.5 py-0.5 text-[10px] font-mono text-primary hover:bg-white/[0.04] hover:underline underline-offset-2 transition-colors"
      title="Read the message in the chat — the journal entry records only its length"
    >
      <MessageSquare className="h-3 w-3 opacity-70" />
      Open chat
    </Link>
  )
}
