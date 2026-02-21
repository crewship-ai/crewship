import { z } from "zod"

export const activityItemSchema = z.object({
  id: z.string(),
  type: z.enum(["assignment", "peer_conversation", "escalation"]),
  status: z.string(),
  summary: z.string(),
  detail: z.string().nullable(),
  from_name: z.string(),
  from_slug: z.string(),
  to_name: z.string().nullable(),
  to_slug: z.string().nullable(),
  crew_name: z.string(),
  crew_slug: z.string(),
  crew_color: z.string().nullable(),
  created_at: z.string(),
})

export type ActivityItem = z.infer<typeof activityItemSchema>
