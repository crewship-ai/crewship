import { z } from "zod"

export const escalationSchema = z.object({
  id: z.string(),
  type: z.enum(["TEXT", "CREDENTIAL", "LINK"]).default("TEXT"),
  from_name: z.string(),
  from_slug: z.string(),
  reason: z.string(),
  context: z.string().nullable(),
  metadata: z.string().nullable(),
  peer_conversation_id: z.string().nullable(),
  status: z.enum(["PENDING", "RESOLVED"]),
  resolution: z.string().nullable(),
  action: z.enum(["approve", "reject", "redirect"]).nullable().default("approve"),
  redirect_to: z.string().nullable().default(null),
  resolved_by: z.string().nullable(),
  resolved_at: z.string().nullable(),
  created_at: z.string(),
})

export interface EvidencePack {
  task_title?: string
  task_id?: string
  agent_slug?: string
  agent_actions?: string[]
  error?: string
  relevant_files?: string[]
  confidence?: number
  suggested_action?: string
}

const EVIDENCE_PACK_KEYS = [
  "task_title", "task_id", "agent_slug", "agent_actions",
  "error", "relevant_files", "confidence", "suggested_action",
] as const

export function parseEvidencePack(metadata: string | null): EvidencePack | null {
  if (!metadata) return null
  try {
    const parsed = JSON.parse(metadata)
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null
    // Detect evidence pack by key presence (not truthiness — confidence: 0 is valid).
    const hasKey = EVIDENCE_PACK_KEYS.some((key) => key in (parsed as Record<string, unknown>))
    return hasKey ? (parsed as EvidencePack) : null
  } catch {
    return null
  }
}

export type Escalation = z.infer<typeof escalationSchema>
