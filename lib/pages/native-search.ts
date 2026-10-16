import { z } from "zod"

const identity = z.string().regex(/^[A-Za-z0-9_-]{1,128}$/)
const match = z.object({
  page_id: identity,
  slug: z.string().regex(/^[a-z0-9][a-z0-9_-]{0,63}$/),
  name: z.string().max(4096),
  content_type: z.enum(["document", "slides"]),
  schema_version: z.number().int().min(1).max(4),
  source_revision: z.number().int().nonnegative(),
  source_generation: z.number().int().nonnegative(),
  snippet: z.string().refine(value => new TextEncoder().encode(value).length <= 512),
}).strict().refine(value => value.content_type === "document" || value.schema_version <= 2)
export const nativePageSearchResponseSchema = z.object({
  scope: z.literal("native-docs-slides"),
  matches: z.array(match).max(50),
  has_more: z.boolean(),
  cursor: z.string().regex(/^[A-Za-z0-9_-]{43}$/).optional(),
}).strict().refine(value => value.has_more === Boolean(value.cursor) && (!value.has_more || value.matches.length > 0))
export type NativePageSearchMatch = z.infer<typeof match>
