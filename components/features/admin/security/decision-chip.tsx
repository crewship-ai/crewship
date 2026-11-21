import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"

const TONE: Record<string, StatusTone> = { ALLOW: "success", DENY: "danger", ESCALATE: "warn", PENDING: "muted" }
/** The decision in its one word: what the judge did, not the stored enum. */
export const DECISION_WORD: Record<string, string> = { ALLOW: "Allowed", DENY: "Denied", ESCALATE: "Escalated", PENDING: "Waiting" }

export function DecisionChip({ decision }: { decision: string | null | undefined }) {
  const d = decision ?? "PENDING"
  return <StatusPill tone={TONE[d] ?? TONE.PENDING} label={DECISION_WORD[d] ?? d} />
}
