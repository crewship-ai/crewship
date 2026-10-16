"use client"

import type { DecisionRef } from "@/lib/chat-e2-cards"
import { Button } from "@/components/ui/button"

/** Complete authorized source snapshot, shared with the conversation cards.
 * An empty/evicted snapshot renders nothing, including no count or empty hint.
 * Opening delegates to the same source detail; Work owns no answer state.
 */
export function DecisionWaitingSection({ references, rechecking, onOpen }: {
  references: DecisionRef[]
  rechecking: boolean
  onOpen: (reference: DecisionRef) => void
}) {
  const pending = references.filter(reference => reference.decision.status === "pending")
  if (pending.length === 0) return null
  return <section aria-label="Waiting for you" className="space-y-2">
    <h3 className="text-sm font-medium">Waiting for you</h3>
    <ul className="space-y-2">{pending.map(reference => <li key={JSON.stringify([reference.decision.source.type, reference.decision.source.id])}>
      <Button type="button" variant="outline" disabled={rechecking} className="h-auto min-h-9 w-full justify-start whitespace-normal text-left coarse:min-h-12 [overflow-wrap:anywhere]" onClick={() => onOpen(reference)}>{reference.decision.title}</Button>
    </li>)}</ul>
  </section>
}
