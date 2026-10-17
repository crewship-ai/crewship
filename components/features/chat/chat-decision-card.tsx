"use client"

import type { DecisionRef } from "@/lib/chat-e2-cards"
import { useChatDecisionAnswer } from "@/hooks/use-chat-decision-answer"
import { Button } from "@/components/ui/button"
import { DecisionEntityCard } from "./decision-entity-card"

/** The source owns the answer; chat and Work consume the same authorized snapshot. */
export function ChatDecisionCard({ reference, chatId, userId, rechecking, onChanged, onAccessLost, onOpen }: {
  reference: DecisionRef; chatId: string; userId: string; rechecking: boolean;
  onChanged: () => void; onAccessLost: () => void; onOpen?: () => void;
}) {
  const reply = useChatDecisionAnswer({ workspaceId: reference.entity.workspace_id,
    userId, chatId, reference, onChanged, onUnauthorized: onAccessLost })
  return <div className="min-w-0 space-y-2">
    {onOpen && <Button variant="outline" size="sm" className="coarse:min-h-[44px]" onClick={onOpen}>Open on right</Button>}
    <DecisionEntityCard reference={reference} rechecking={rechecking}
      onAnswer={reply.answer} onRecheck={onChanged} recovery={reply.recovery}
      onReviewConflict={reply.reviewConflict} feedback={reply.message} />
  </div>
}
