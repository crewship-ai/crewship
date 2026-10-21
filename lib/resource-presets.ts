import { z } from "zod"
import { resourceRightSchema } from "./resource-sharing"
import { presetSelectionSchema } from "./resource-preset-selection"

const grant = resourceRightSchema.extend({ issuer_id: z.string(), issuer_name: z.string().optional(), created_at: z.string(), live: z.boolean() })
export const presetCatalogSchema = z.object({ presets: z.array(z.object({
  definition_hash: z.string().min(1),
  definition: z.object({ preset_id: z.string().min(1), version: z.number().int().positive(), kind: z.enum(["project", "routine"]),
    managed_operations: z.array(z.string()), levels: z.array(z.object({ level: z.string().min(1), operations: z.array(z.string()) })),
  }),
})) })
export const presetPreviewSchema = z.object({
  selection: z.object({ membership_id: z.string().min(1), revision: z.number().int().positive(), resource: z.object({kind:z.enum(["project","routine"]),id:z.string().min(1)}),
    preset_id:z.string().min(1),preset_version:z.number().int().positive(),level:z.string().min(1),reissue:z.array(resourceRightSchema),
  }),
  definition_hash:z.string().min(1),current_selection:presetSelectionSchema.nullable(),before:z.array(grant),
  changes:z.array(z.object({right:resourceRightSchema,action:z.enum(["add","remove","retain","reissue"]),issuer_id:z.string(),created_at:z.string().optional(),live:z.boolean()})),
  after_rights:z.array(resourceRightSchema),orthogonal_rights:z.array(grant),
  warnings:z.array(z.object({code:z.string(),message:z.string(),before_eligible:z.boolean().optional(),after_eligible:z.boolean().optional()})),
  confirmation_digest:z.string().regex(/^[a-f0-9]{64}$/),
})
export type PresetPreview = z.infer<typeof presetPreviewSchema>
