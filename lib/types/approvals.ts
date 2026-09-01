import { z } from "zod"

/** Supported approval kinds — used for colour-coding the kind badge. */
export const APPROVAL_KINDS = [
  "destructive_op",
  "cost_threshold",
  "target_environment",
  "tool_call",
  "ephemeral_hire",
  "custom",
] as const
export type ApprovalKind = (typeof APPROVAL_KINDS)[number]

export const APPROVAL_STATUSES = ["pending", "approved", "denied", "all"] as const
export type ApprovalStatus = (typeof APPROVAL_STATUSES)[number]

export const approvalRowSchema = z.object({
  id: z.string(),
  kind: z.string(),
  reason: z.string().optional().default(""),
  requested_by: z.string().optional(),
  status: z.string(),
  created_at: z.string(),
  timeout_at: z.string().optional().nullable(),
  decided_at: z.string().optional().nullable(),
  decided_by: z.string().optional().nullable(),
  // The response field is decision_comment (the approvals_queue column
  // and the Go struct field); the *request* body still sends `comment`.
  decision_comment: z.string().optional().nullable(),
  crew_id: z.string().optional().nullable(),
  agent_id: z.string().optional().nullable(),
  mission_id: z.string().optional().nullable(),
  // Payload is free-form JSON — allowed to use `any` shape here.
  payload: z.record(z.string(), z.unknown()).optional(),
})
export type ApprovalRow = z.infer<typeof approvalRowSchema>

// Every key here is required, and that is not defensive taste — it mirrors the
// envelope's `required` list in cmd/gen-openapi. ApprovalsHandler.List writes
// `{rows, status, count, has_more}` as a map literal, so all four are on the
// wire unconditionally and none of them is `omitempty`.
//
// They were optional, which cost something concrete: `useApprovals({loadAll})`
// pages until `has_more` is false, and an envelope that simply lacked the key
// parsed clean and read as `undefined` — so a drifted server would silently
// serve page one of the approval history and the UI would show it as the whole
// history, with no error anywhere. Optional here means "accept a shape the
// server never sends and degrade quietly", which is the failure this branch is
// about. If a field ever does become conditional, it becomes `.optional()`
// here and non-required in the generator in the same commit, never one alone.
export const approvalListResponseSchema = z.object({
  rows: z.array(approvalRowSchema),
  status: z.string(),
  count: z.number(),
  has_more: z.boolean(),
})
export type ApprovalListResponse = z.infer<typeof approvalListResponseSchema>

export const approvalDecideResponseSchema = z.object({
  status: z.string(),
  decided_by: z.string().optional(),
})
export type ApprovalDecideResponse = z.infer<typeof approvalDecideResponseSchema>
