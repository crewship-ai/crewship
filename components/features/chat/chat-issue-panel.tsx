"use client"

import { useCallback, useMemo, useState } from "react"
import { ArrowLeft } from "lucide-react"
import { Button } from "@/components/ui/button"
import { IssueDetailSurface } from "@/components/features/issues/issue-detail-surface"
import type { EntityRef } from "@/lib/chat-entity-cards"
import { useChatIssueMutation } from "@/hooks/use-chat-issue-mutation"
import { CardWriteFeedback } from "./issue-entity-card"

export type PanelTarget = { kind: "issue"; id: string; identifier: string; workspaceId: string }

export function ChatIssuePanel({ target, reference, rechecking, checkedAt, reconcile, onClose }: {
  target: PanelTarget; reference?: EntityRef; rechecking: boolean; checkedAt?: number; reconcile: () => void; onClose: () => void
}) {
  const [resetKey, setResetKey] = useState(0)
  const mutation = useChatIssueMutation(reconcile, reconcile)
  const { submit } = mutation
  const patch = useCallback((path: string, body: Record<string, unknown>, revision: number | undefined) => {
    if (!revision) return Promise.resolve(false)
    return submit(path, "PATCH", { ...body, expected_revision: revision })
  }, [submit])
  const comment = useCallback((path: string, body: Record<string, unknown>) => submit(path, "POST", body), [submit])
  const write = useMemo(() => ({ patch, comment, resetKey, saved: mutation.state === "saved", blocked: ["saving", "unknown", "conflict"].includes(mutation.state) }), [patch, comment, resetKey, mutation.state])
  return <div className="flex h-full min-h-0 flex-col">
    <header className="shrink-0 border-b p-2"><Button variant="ghost" size="sm" className="coarse:min-h-12" onClick={onClose}><ArrowLeft className="size-3.5" />Back to Work</Button></header>
    <div className="min-h-0 flex-1 overflow-auto">
      <IssueDetailSurface workspaceId={target.workspaceId} identifier={target.identifier}
        actions={reference?.current?.allowed_actions ?? []} authorizationKey={checkedAt}
        rechecking={rechecking || !reference} onUnauthorized={reconcile}
        onChanged={reconcile} write={write}
        feedback={<CardWriteFeedback {...mutation} resolveConflict={() => { mutation.resolveConflict(); setResetKey((key) => key + 1) }} />} />
    </div>
  </div>
}
