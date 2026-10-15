import { z } from "zod"
import { routineHref } from "./routine-href"

const snapshotSchema = z.object({ slug: z.string().min(1), draft_id: z.string().min(1), draft_revision: z.number().int().positive() })

export function routineDraftReviewLink(payload: unknown): string | null {
  const result = snapshotSchema.safeParse(payload)
  if (!result.success) return null
  const { slug, draft_id, draft_revision } = result.data
  return `${routineHref(slug)}&view=publish&draft_review=${encodeURIComponent(`${draft_id}:${draft_revision}`)}`
}
