"use client"

import { useMemo, useRef, useState } from "react"
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso"
import { ArrowDownIcon, Bot } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ConversationEmptyState } from "@/components/ai-elements/conversation"
import type { ChatTurn } from "@/hooks/use-chat"
import { TurnRenderer } from "./turn-renderer"

/** Opt-in flag for the virtualized chat list. The classic path renders every
 *  turn inside StickToBottom + AnimatePresence, which keeps hundreds of
 *  animated nodes mounted on long conversations; the virtual path mounts
 *  only the viewport. Off by default until the streaming/scroll UX has
 *  soaked — enable with `localStorage.setItem("crewship.virtualChat", "1")`.
 *
 *  Known tradeoffs vs the classic path (why this is flagged):
 *  - no pin-to-top spacer (the just-sent question doesn't anchor to the
 *    viewport top while the reply streams; follow-bottom is used instead),
 *  - no popLayout exit animations for removed turns,
 *  - conversation-search jump (data-turn-id querySelector + scrollIntoView)
 *    can't reach unmounted off-window turns — needs virtuoso scrollToIndex
 *    threading before the flag defaults on,
 *  - scroll restore after a session switch relies on followOutput; a
 *    history *replace* may land mid-list (wire scrollToIndex on
 *    historyLoading→false before defaulting on).
 */
export function virtualChatEnabled(): boolean {
  if (typeof window === "undefined") return false
  try {
    return window.localStorage.getItem("crewship.virtualChat") === "1"
  } catch {
    return false
  }
}

interface VirtualConversationProps {
  turns: ChatTurn[]
  sessionId: string
  agentId: string
  agentName?: string
  historyLoading: boolean
  isStreaming: boolean
  animateAfter: number
  onCopy: (s: string) => void
  onFileClick: (s: string) => void
  onRegenerate?: () => void
  onEditUserMessage?: (turnId: string, newContent: string) => void
  resolveAuthorName: (userId: string) => string | null
  /** Rendered under the last turn (streaming indicator). */
  footer?: React.ReactNode
}

/**
 * Virtualized message list (react-virtuoso). Dynamic row heights are
 * re-measured automatically, so the streaming mutation of the last turn
 * grows its row live; `followOutput` keeps the viewport glued to the bottom
 * only while the user is already there — scrolling up to read history is
 * never hijacked by incoming tokens.
 */
// Stable component map for Virtuoso: an inline `Footer: () => …` closure is
// a NEW component type every render, so React remounted the footer subtree
// per token batch while streaming (StreamingIndicator's fade-in replayed —
// visible flicker). The footer node travels through Virtuoso's `context`
// prop instead; the type stays referentially stable.
type VirtuosoCtx = { footer: React.ReactNode }
const VirtuosoFooter = ({ context }: { context?: VirtuosoCtx }) => (
  <div className="mx-auto w-full max-w-3xl px-4">{context?.footer}</div>
)
const VIRTUOSO_COMPONENTS = { Footer: VirtuosoFooter }

export function VirtualConversation({
  turns,
  sessionId,
  agentId,
  agentName,
  historyLoading,
  isStreaming,
  animateAfter,
  onCopy,
  onFileClick,
  onRegenerate,
  onEditUserMessage,
  resolveAuthorName,
  footer,
}: VirtualConversationProps) {
  const virtuosoRef = useRef<VirtuosoHandle>(null)
  const [atBottom, setAtBottom] = useState(true)

  // Key rows by turn id so session swaps and history reloads never recycle
  // a row's measured height for a different turn.
  const computeItemKey = useMemo(() => {
    return (index: number) => turns[index]?.id ?? index
  }, [turns])

  if (turns.length === 0 && !historyLoading) {
    return (
      <div className="relative flex-1 overflow-y-hidden" role="log">
        <ConversationEmptyState
          icon={<Bot className="h-12 w-12" />}
          title="Start a conversation"
          description={agentName ? `Send a message to ${agentName}` : "Send a message or pick a suggestion below"}
        />
      </div>
    )
  }

  return (
    <div className="relative flex-1 min-h-0" role="log">
      <Virtuoso
        key={sessionId}
        ref={virtuosoRef}
        data={turns}
        computeItemKey={computeItemKey}
        // Keep following new output only when already at the bottom; smooth
        // while streaming so token growth doesn't judder.
        followOutput={(isAtBottom) => (isAtBottom ? (isStreaming ? "smooth" : "auto") : false)}
        atBottomStateChange={setAtBottom}
        initialTopMostItemIndex={Math.max(0, turns.length - 1)}
        increaseViewportBy={{ top: 600, bottom: 600 }}
        className="h-full"
        context={{ footer }}
        components={VIRTUOSO_COMPONENTS}
        itemContent={(idx, turn) => (
          <div className="mx-auto w-full max-w-3xl px-4 pb-8">
            <TurnRenderer
              turn={turn}
              onCopy={onCopy}
              onFileClick={onFileClick}
              isLastAssistant={turn.role === "assistant" && idx === turns.length - 1}
              onRegenerate={turn.role === "assistant" && idx === turns.length - 1 && !isStreaming ? onRegenerate : undefined}
              onEditUserMessage={onEditUserMessage}
              animateAfter={animateAfter}
              agentId={agentId}
              chatId={sessionId}
              resolveAuthorName={resolveAuthorName}
            />
          </div>
        )}
      />
      {!atBottom && (
        <Button
          className="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full"
          onClick={() => virtuosoRef.current?.scrollToIndex({ index: turns.length - 1, align: "end", behavior: "smooth" })}
          size="icon"
          type="button"
          variant="outline"
          aria-label="Scroll to bottom"
        >
          <ArrowDownIcon className="size-4" />
        </Button>
      )}
    </div>
  )
}
