import type { ChatTurn } from "@/hooks/use-chat"
import type { IssuePriority, MissionStatus } from "@/lib/types/mission"

export type IssueAction = "view" | "comment" | "edit_fields" | "assign" | "change_status" | "change_workflow" | "attach_files" | "link" | "start_work" | "delete"
export interface EntityRef {
  event_id: string
  operation_id: string
  kind: string
  verb: string
  entity: { id: string; identifier: string | null; workspace_id: string; deleted?: boolean }
  current?: { title: string; status: MissionStatus; priority: IssuePriority; assignee_type: string | null; assignee_id: string | null; revision: number; aggregate_revision: number; allowed_actions: IssueAction[] }
  provenance: { actor: { type: string; id: string }; at: string }
  anchor: { trigger_message_id: string; run_id: string | null; run_status: string | null }
  revision_at_action: { revision: number; aggregate_revision: number }
}
export interface EntityRefPage {
  contract_version?: number // E0 snapshots predate the optional E1 marker.
  reset: boolean
  refs: EntityRef[]
  next_cursor: string
  snapshot_id: string
  has_more: boolean
  snapshot_complete: boolean
}

/** Never guess a card's turn from proximity: persisted run identity is authority. */
export function anchorEntityRefs(turns: readonly ChatTurn[], refs: readonly EntityRef[]) {
  const anchored = new Map<string, { refs: EntityRef[]; withoutReply: boolean }>()
  for (const ref of refs) {
    const run = ref.anchor.run_id
    const matching = run ? turns.filter((t) => t.role !== "user" && (t.runId === run || t.metadata?.run_id === run || t.parts.some((p) => p.metadata?.run_id === run))) : []
    const reply = matching.filter((t) => t.role === "assistant" && t.parts.some((p) => p.type === "text" && p.content.trim())).at(-1)
    const target = reply ?? turns.find((t) => t.id === ref.anchor.trigger_message_id)
    if (!target) continue // History may still be loading; never move it to another run.
    const group = anchored.get(target.id) ?? { refs: [], withoutReply: !reply }
    group.refs.push(ref)
    anchored.set(target.id, group)
  }
  return anchored
}
