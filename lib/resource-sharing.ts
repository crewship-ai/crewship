import { z } from "zod"
import { presetSelectionSchema } from "./resource-preset-selection"

export const resourceRightSchema = z.object({
  kind: z.enum(["agent", "project", "routine", "crew"]), id: z.string().min(1), operation: z.string(),
})
export const resourcePolicySchema = z.object({
  membership_id: z.string().min(1), revision: z.number().int().positive(),
  mode: z.enum(["trusted", "restricted"]), rights: z.array(resourceRightSchema),
  preset_selections: z.array(presetSelectionSchema).optional(),
  grant_details: z.array(resourceRightSchema.extend({
    issuer_id: z.string(), issuer_name: z.string().optional(), created_at: z.string(), live: z.boolean(),
  })).optional(),
})
export type ResourcePolicy = z.infer<typeof resourcePolicySchema>
export type SharedResource = { kind: "routine" | "project" | "crew"; id: string; name: string }
export const shareOperations = { crew: ["view", "use"], routine: ["view", "results", "run", "edit", "publish"], project: ["list", "read", "contribute", "manage", "delete_issues"] } as const
export const isResourceRight = (right: z.infer<typeof resourceRightSchema>, resource: SharedResource) =>
  right.kind === resource.kind && right.id === resource.id

export const resourceShareOperations = (policy: ResourcePolicy, resource: SharedResource): readonly string[] =>
  policy.mode === "trusted" ? (resource.kind === "crew" ? ["manage"] : []) : shareOperations[resource.kind]

/** Preserve every other resource and unknown future operation; never convert accounts. */
export function resourceShareReplacement(policy: ResourcePolicy, resource: SharedResource, operations: string[], reissue: string[]) {
  if (policy.mode !== "restricted" && resource.kind !== "crew") throw new Error("Trusted access is managed through workspace roles")
  if (resource.kind === "routine" && operations.includes("publish") && !operations.includes("edit")) throw new Error("Publish requires Edit on the same routine")
  const editable = new Set<string>(resourceShareOperations(policy, resource))
  if ([...operations, ...reissue].some(op => !editable.has(op)) || reissue.some(op => !operations.includes(op))) {
    throw new Error("Invalid resource operation")
  }
  const selected = [...new Set(operations)].map(operation => ({ kind: resource.kind, id: resource.id, operation }))
  return {
    membership_id: policy.membership_id, revision: policy.revision, mode: policy.mode,
    rights: [...policy.rights.filter(right => !isResourceRight(right, resource) || !editable.has(right.operation)), ...selected],
    ...(reissue.length ? { reissue: selected.filter(right => reissue.includes(right.operation)) } : {}),
  }
}

export const resourceOperationLabel = (operation: string) => operation === "results" ? "View runs and outputs" : operation === "delete_issues" ? "Delete issues" : operation === "edit" ? "Edit draft" : operation === "publish" ? "Publish" : operation
