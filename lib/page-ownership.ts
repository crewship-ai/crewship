import { z } from "zod"

export const pageOwnershipSchema = z.object({
  id: z.string().min(1),
  slug: z.string().min(1),
  name: z.string(),
  type: z.enum(["artifact", "board", "document", "slides"]),
  owner_user_id: z.string().min(1),
  owner_name: z.string(),
  eligible_successors: z.array(z.object({ user_id: z.string().min(1), name: z.string(), email: z.string() })),
})
export const pageOwnershipListSchema = z.object({ pages: z.array(pageOwnershipSchema), next_cursor: z.string().optional() })
export type PageOwnershipList = z.infer<typeof pageOwnershipListSchema>
export type PageOwnership = z.infer<typeof pageOwnershipSchema>
export interface PageOwnershipTransfer { pageId: string; expected_owner_user_id: string; successor_user_id: string }
export class PageOwnershipError extends Error {
  constructor(message: string, public status?: number) { super(message) }
}
export const ownershipAccessDenied = (error: unknown) => error instanceof PageOwnershipError && [401, 403, 404].includes(error.status ?? 0)

export const pageOwnershipLabels: Record<PageOwnership["type"], string> = { artifact: "Artifact", board: "Board", document: "Doc", slides: "Slides" }
