import { z } from "zod"

export const escalationSchema = z.object({
  id: z.string(),
  from_name: z.string(),
  from_slug: z.string(),
  reason: z.string(),
  context: z.string().nullable(),
  peer_conversation_id: z.string().nullable(),
  status: z.enum(["PENDING", "RESOLVED"]),
  resolution: z.string().nullable(),
  resolved_at: z.string().nullable(),
  created_at: z.string(),
})

export type Escalation = z.infer<typeof escalationSchema>
