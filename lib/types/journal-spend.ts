import { z } from "zod"

/**
 * Response shape for GET /api/v1/journal/spend (#1404) — mirrors
 * internal/journal/spend.go's SpendResult. Deliberately a separate,
 * journal-native rollup from lib/types/paymaster.ts (which backs
 * /api/v1/paymaster/*) — see docs/guides/crew-journal.mdx's Spend
 * section for why two cost surfaces coexist.
 */

export const SPEND_WINDOWS = ["24h", "7d", "30d"] as const
export type SpendWindow = (typeof SPEND_WINDOWS)[number]

export const spendByAgentBucketSchema = z.object({
  date: z.string(),
  crew_id: z.string(),
  agent_id: z.string(),
  cost_usd: z.number(),
  call_count: z.number(),
})
export type SpendByAgentBucket = z.infer<typeof spendByAgentBucketSchema>

export const spendByRoutineBucketSchema = z.object({
  date: z.string(),
  pipeline_id: z.string(),
  pipeline_slug: z.string(),
  cost_usd: z.number(),
  run_count: z.number(),
})
export type SpendByRoutineBucket = z.infer<typeof spendByRoutineBucketSchema>

export const spendTopRowSchema = z.object({
  kind: z.enum(["routine", "run"]),
  id: z.string(),
  label: z.string(),
  cost_usd: z.number(),
})
export type SpendTopRow = z.infer<typeof spendTopRowSchema>

export const spendResponseSchema = z.object({
  window: z.string(),
  total_cost_usd: z.number(),
  by_agent: z.array(spendByAgentBucketSchema),
  by_routine: z.array(spendByRoutineBucketSchema),
  top_routines: z.array(spendTopRowSchema),
  top_runs: z.array(spendTopRowSchema),
  truncated: z.boolean(),
})
export type SpendResponse = z.infer<typeof spendResponseSchema>

export const EMPTY_SPEND_RESPONSE: SpendResponse = {
  window: "24h",
  total_cost_usd: 0,
  by_agent: [],
  by_routine: [],
  top_routines: [],
  top_runs: [],
  truncated: false,
}
