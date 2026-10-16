"use client"

import { apiFetch } from "@/lib/api-fetch"
import { ProposalRefSchema, type ProposalRef } from "@/lib/chat-e2-cards"
import type { ProposalScope } from "@/hooks/use-proposal-draft"
import { ProposalEntityCard } from "./proposal-entity-card"

export function ChatProposalCard({ reference, chatId, userId, surface, rechecking, assignees, onSnapshot, onAccessLost, onComment, onOpen, renderIssue }: {
  reference: ProposalRef; chatId: string; userId: string; surface: ProposalScope["surface"];
  rechecking: boolean; assignees: { id: string; name: string }[];
  onSnapshot: (reference: ProposalRef) => void; onAccessLost: () => void;
  onComment: (comment: string) => Promise<boolean>; onOpen?: () => void;
  renderIssue: (issue: { id: string; identifier: string }) => React.ReactNode;
}) {
  return <ProposalEntityCard reference={reference} viewerId={userId} surface={surface}
    rechecking={rechecking} assignees={assignees} onSnapshot={onSnapshot} onAccessLost={onAccessLost}
    onComment={onComment} onOpen={onOpen} renderIssue={renderIssue}
    onReadCurrent={async () => {
      if (rechecking) return null
      try {
        const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/proposals/${encodeURIComponent(reference.entity.id)}?workspace_id=${encodeURIComponent(reference.entity.workspace_id)}`, { cache: "no-store" })
        if ([401, 403, 404].includes(response.status)) { onAccessLost(); return null }
        if (!response.ok) return null
        const snapshot = ProposalRefSchema.parse(await response.json())
        return snapshot.entity.id === reference.entity.id && snapshot.entity.workspace_id === reference.entity.workspace_id ? snapshot : null
      } catch { return null }
    }}
    transport={request => {
      if (rechecking) return Promise.reject(new Error("Current access is not verified"))
      const path = `/api/v1/chats/${encodeURIComponent(chatId)}/proposals/${encodeURIComponent(reference.entity.id)}`
      return apiFetch(`${path}${request.action === "edit" ? "" : `/${request.action}`}?workspace_id=${encodeURIComponent(reference.entity.workspace_id)}`, {
        method: request.action === "edit" ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json", "Idempotency-Key": request.key },
        body: request.body,
      })
    }} />
}
