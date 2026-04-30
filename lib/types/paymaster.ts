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

/**
 * One row of the Subscription plans panel — flat-rate credentials don't
 * have a $ figure (the user already paid the subscription) but we surface
 * the usage signal: how many calls, how many tokens, when last used.
 * `subscription_plan` is the human label set by orchestrator/exec_env.go
 * (e.g. "Anthropic Max"), or "unknown" for pre-migration / mis-tagged rows.
 */
export const subscriptionUsageRowSchema = z.object({
  subscription_plan: z.string(),
  provider: z.string(),
  call_count: z.number(),
  input_tokens: z.number(),
  output_tokens: z.number(),
  last_ts: z.string(),
})
export type SubscriptionUsageRow = z.infer<typeof subscriptionUsageRowSchema>

export const subscriptionUsageResponseSchema = z.object({
  rows: z.array(subscriptionUsageRowSchema),
  since: z.string().optional(),
  until: z.string().optional(),
})
export type SubscriptionUsageResponse = z.infer<
  typeof subscriptionUsageResponseSchema
>

/** Cost-confidence labels mirror the Helicone "precise / estimate / unknown"
 *  pattern. Adopted in migration v60 so every $ figure surfaced in the UI
 *  carries provenance — never just a number, always a number + a badge. */
export const COST_CONFIDENCE = ["precise", "estimate", "unknown"] as const
export type CostConfidence = (typeof COST_CONFIDENCE)[number]

/** Billing mode discriminates metered (per-token $) from flat-rate
 *  (subscription) ledger rows. The dashboard splits them visually. */
export const BILLING_MODES = ["metered", "flat_rate"] as const
export type BillingMode = (typeof BILLING_MODES)[number]
