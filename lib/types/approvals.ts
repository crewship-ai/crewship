import { z } from "zod"

/** Supported approval kinds — used for colour-coding the kind badge. */
export const APPROVAL_KINDS = [
  "destructive_op",
  "cost_threshold",
  "target_environment",
  "tool_call",
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
  comment: z.string().optional().nullable(),
  crew_id: z.string().optional().nullable(),
  agent_id: z.string().optional().nullable(),
  mission_id: z.string().optional().nullable(),
  // Payload is free-form JSON — allowed to use `any` shape here.
  payload: z.record(z.string(), z.unknown()).optional(),
})
export type ApprovalRow = z.infer<typeof approvalRowSchema>

export const approvalListResponseSchema = z.object({
  rows: z.array(approvalRowSchema),
  status: z.string().optional(),
  count: z.number().optional(),
})
export type ApprovalListResponse = z.infer<typeof approvalListResponseSchema>

export const approvalDecideResponseSchema = z.object({
  status: z.string(),
  decided_by: z.string().optional(),
})
export type ApprovalDecideResponse = z.infer<typeof approvalDecideResponseSchema>
