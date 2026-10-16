import { z } from "zod"

// JSON Schema lengths count Unicode code points, as does the Go validator.
const text = (min: number, max: number) => z.string().refine((s) => {
  const n = Array.from(s).length
  return n >= min && n <= max
}).meta({ minLength: min, maxLength: max })
const id = text(1, 200)
const timestamp = z.iso.datetime({ offset: true })
const unique = <T>(values: T[]) => new Set(values).size === values.length
const item = z.strictObject({ id, title: text(1, 240), description: text(0, 10000), crew_id: id,
  project_id: id.nullable(), assignee_id: id.nullable(), priority: z.enum(["none", "urgent", "high", "medium", "low"]), selected: z.boolean() })
export const ProposalCardSchema = z.strictObject({
  schema_version: z.literal(1), version: z.number().int().min(1), status: z.enum(["draft", "accepted", "discarded", "expired"]),
  title: text(1, 240), items: z.array(item).min(1).max(20), expires_at: timestamp,
  allowed_actions: z.array(z.enum(["view", "edit", "confirm", "discard"])),
  outcome: z.strictObject({ reviewed_version: z.number().int().min(1), confirmed_by: id, confirmed_at: timestamp,
    issues: z.array(z.strictObject({ id, identifier: id })).min(1).max(20) }).nullable(),
}).superRefine((p, ctx) => {
  const allowed = p.status === "draft" ? ["view", "edit", "confirm", "discard"] : ["view"]
  const valid = unique(p.items.map((i) => i.id)) && unique(p.allowed_actions) && p.allowed_actions.includes("view") && p.allowed_actions.every((a) => allowed.includes(a)) &&
    (p.status === "accepted" ? p.outcome !== null && p.outcome.reviewed_version <= p.version && p.outcome.issues.length === p.items.filter((i) => i.selected).length && unique(p.outcome.issues.map((i) => i.id)) : p.outcome === null)
  if (!valid) ctx.addIssue({ code: "custom", message: "Invalid proposal state or receipt" })
})
const fieldBase = { id: z.string().regex(/^[A-Za-z][A-Za-z0-9_-]{0,63}$/), label: text(1, 240), required: z.boolean() }
export const DecisionFieldSchema = z.discriminatedUnion("type", [
  z.strictObject({ ...fieldBase, type: z.enum(["text", "textarea", "boolean", "number", "datetime"]) }),
  z.strictObject({ ...fieldBase, type: z.literal("choice"), options: z.array(z.strictObject({ value: id, label: text(1, 240) })).min(1).max(20) }),
])
export const DecisionCardSchema = z.strictObject({
  schema_version: z.literal(1), source: z.strictObject({ type: z.enum(["escalation", "waitpoint", "run_approval"]), id, version: text(1, 128) }),
  decision_type: z.enum(["information", "workflow", "whole_run_approval"]), status: z.enum(["pending", "answered", "approved", "rejected", "expired", "cancelled"]),
  workflow_actions: z.array(z.strictObject({ id: z.string().regex(/^[A-Za-z][A-Za-z0-9_-]{0,63}$/), label: text(1, 240), approved: z.boolean() })).min(2).max(20).optional(),
  title: text(1, 240), fields: z.array(DecisionFieldSchema).max(20), expires_at: timestamp.nullable(),
  allowed_actions: z.array(z.enum(["view", "answer", "approve", "reject"])),
  resolution: z.strictObject({ action_id: id.optional(), answered_by: id, answered_at: timestamp,
    values: z.record(z.string().regex(/^[A-Za-z][A-Za-z0-9_-]{0,63}$/), z.union([text(0, 10000), z.boolean(), z.number()])) }).nullable(),
}).superRefine((d, ctx) => {
  const approval = d.decision_type === "whole_run_approval"
  const pending = d.status === "pending"
  const closedWithoutAnswer = ["pending", "expired", "cancelled"].includes(d.status)
  const allowed = pending ? approval ? ["view", "approve", "reject"] : d.decision_type === "workflow" ? ["view", "answer", "approve", "reject"] : ["view", "answer"] : ["view"]
  let valid = !(approval && d.status === "answered" || d.decision_type === "information" && d.status === "approved") && (d.source.type === "run_approval") === approval && unique(d.fields.map((f) => f.id)) && unique(d.allowed_actions) && d.allowed_actions.includes("view") && d.allowed_actions.every((a) => allowed.includes(a)) &&
    (closedWithoutAnswer ? d.resolution === null : d.resolution !== null)
  if (d.workflow_actions) {
    valid &&= d.decision_type === "workflow" && unique(d.workflow_actions.map((a) => a.id))
    if (d.resolution) {
      const action = d.workflow_actions.find((a) => a.id === d.resolution?.action_id)
      valid &&= !!action && (d.status === "rejected" ? !action.approved : action.approved)
    }
  } else valid &&= !d.resolution?.action_id
  for (const f of d.fields) if (f.type === "choice" && !unique(f.options.map((o) => o.value))) valid = false
  for (const [key, value] of Object.entries(d.resolution?.values ?? {})) {
    const f = d.fields.find((f) => f.id === key)
    if (!f) { valid = false; continue }
    if (f.type === "boolean" || f.type === "number") valid &&= typeof value === f.type
    else {
      valid &&= typeof value === "string" && (!f.required || value !== "")
      if (f.type === "choice") valid &&= f.options.some((o) => o.value === value)
      if (f.type === "datetime") valid &&= timestamp.safeParse(value).success
    }
  }
  if (d.status === "answered" || d.status === "approved") for (const f of d.fields) if (f.required && !Object.hasOwn(d.resolution?.values ?? {}, f.id)) valid = false
  if (!valid) ctx.addIssue({ code: "custom", message: "Invalid decision state, form or source scope" })
})
const base = {
  event_id: id, operation_id: text(0, 200), verb: text(1, 64),
  entity: z.strictObject({ id, identifier: text(0, 200), workspace_id: id }),
  provenance: z.strictObject({ actor: z.strictObject({ type: z.enum(["agent", "human", "system"]), id }), at: timestamp }),
  anchor: z.strictObject({ trigger_message_id: id, run_id: id, run_status: text(1, 64).nullable() }),
  revision_at_action: z.strictObject({ revision: z.number().int().min(0), aggregate_revision: z.number().int().min(0) }),
}
export const ProposalRefSchema = z.strictObject({ ...base, kind: z.literal("proposal"),
  provenance: z.strictObject({ actor: z.strictObject({ type: z.literal("agent"), id }), at: timestamp }), proposal: ProposalCardSchema })
export const DecisionRefSchema = z.strictObject({ ...base, kind: z.literal("decision"), decision: DecisionCardSchema }).refine((r) => r.entity.id === r.decision.source.id, "Source identity mismatch")
export const E2CardSchema = z.discriminatedUnion("kind", [ProposalRefSchema, DecisionRefSchema])
export const E2CardPageSchema = z.strictObject({ contract_version: z.literal(1), reset: z.boolean(), next_cursor: text(0, 4096),
  snapshot_id: text(1, 200), snapshot_complete: z.boolean(), has_more: z.boolean(), refs: z.array(E2CardSchema).max(100) })
export const E2VersionConflictSchema = z.strictObject({ error: z.enum(["proposal_version_conflict", "decision_version_conflict"]), current: E2CardSchema })
export type ProposalRef = z.infer<typeof ProposalRefSchema>
export type DecisionRef = z.infer<typeof DecisionRefSchema>
export type E2CardRef = z.infer<typeof E2CardSchema>
export type E2CardPage = z.infer<typeof E2CardPageSchema>

// Structural JSON Schema complements the state-dependent runtime validators.
export const e2CardJSONSchema = () => z.toJSONSchema(E2CardSchema, { reused: "ref" })

const values = z.record(z.string().regex(/^[A-Za-z][A-Za-z0-9_-]{0,63}$/), z.union([text(0, 10000), z.boolean(), z.number()]))
export const E2CommandSchemas = {
  proposal_create: z.strictObject({ title: text(1, 240), items: z.array(item).min(1).max(20) }),
  proposal_edit: z.strictObject({ expected_version: z.number().int().min(1), title: text(1, 240), items: z.array(item).min(1).max(20) }),
  proposal_confirm: z.strictObject({ proposal_id: id, reviewed_version: z.number().int().min(1) }),
  proposal_discard: z.strictObject({ expected_version: z.number().int().min(1) }),
  decision_answer: z.strictObject({ source_id: id, expected_version: text(1, 128), action: z.enum(["answer", "approve", "reject"]), action_id: id.optional(), values }),
}
