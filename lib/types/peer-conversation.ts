import { z } from "zod"

export const peerConversationSchema = z.object({
  id: z.string(),
  from_name: z.string(),
  from_slug: z.string(),
  to_name: z.string(),
  to_slug: z.string(),
  question: z.string(),
  response: z.string().nullable(),
  status: z.enum(["RUNNING", "COMPLETED", "FAILED"]),
  duration_ms: z.number().nullable(),
  escalated: z.boolean(),
  created_at: z.string(),
  finished_at: z.string().nullable(),
})

export type PeerConversation = z.infer<typeof peerConversationSchema>
