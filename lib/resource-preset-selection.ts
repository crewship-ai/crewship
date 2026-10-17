import { z } from "zod"

/** Informational saved intent; canonical grant provenance remains authoritative. */
export const presetSelectionSchema = z.object({
  id: z.string().min(1), kind: z.enum(["project", "routine"]), resource_id: z.string().min(1),
  preset_id: z.string().min(1), version: z.number().int().positive(), level: z.string().min(1),
  definition_hash: z.string().min(1), selected_by: z.string().optional(), selected_at: z.string(),
  state: z.enum(["selected", "custom"]),
})
