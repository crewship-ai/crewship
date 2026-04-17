import { z } from "zod"

/**
 * Time-range tokens supported by `/api/v1/paymaster/spend/*` endpoints.
 * Keep in sync with backend handler's accepted values.
 */
export const PAYMASTER_RANGES = ["1h", "24h", "7d", "30d"] as const
export type PaymasterRange = (typeof PAYMASTER_RANGES)[number]

export const crewSpendRowSchema = z.object({
  crew_id: z.string(),
  crew_name: z.string().optional(),
  cost_usd: z.number(),
  call_count: z.number(),
  total_tokens: z.number(),
})
export type CrewSpendRow = z.infer<typeof crewSpendRowSchema>

export const crewSpendResponseSchema = z.object({
  rows: z.array(crewSpendRowSchema),
  since: z.string().optional(),
  until: z.string().optional(),
})
export type CrewSpendResponse = z.infer<typeof crewSpendResponseSchema>

export const agentSpendRowSchema = z.object({
  agent_id: z.string(),
  agent_name: z.string().optional(),
  cost_usd: z.number(),
  call_count: z.number(),
  total_tokens: z.number(),
})
export type AgentSpendRow = z.infer<typeof agentSpendRowSchema>

export const agentSpendResponseSchema = z.object({
  rows: z.array(agentSpendRowSchema),
  crew_id: z.string().optional(),
})
export type AgentSpendResponse = z.infer<typeof agentSpendResponseSchema>

export const topSpenderRowSchema = z.object({
  scope: z.string().optional(),
  scope_type: z.string().optional(),
  cost_usd: z.number(),
  call_count: z.number(),
  total_tokens: z.number(),
  // Backend returns different shapes depending on scope; unknown fields ignored.
  crew_id: z.string().optional(),
  crew_name: z.string().optional(),
  agent_id: z.string().optional(),
  agent_name: z.string().optional(),
  mission_id: z.string().optional(),
  mission_name: z.string().optional(),
})
export type TopSpenderRow = z.infer<typeof topSpenderRowSchema>

export const topSpendersResponseSchema = z.object({
  rows: z.array(topSpenderRowSchema),
  since: z.string().optional(),
  until: z.string().optional(),
})
export type TopSpendersResponse = z.infer<typeof topSpendersResponseSchema>
