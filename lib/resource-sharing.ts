import { z } from "zod"

export const resourceRightSchema = z.object({
  kind: z.enum(["agent", "project", "routine"]), id: z.string().min(1), operation: z.string(),
})
export const resourcePolicySchema = z.object({
  membership_id: z.string().min(1), revision: z.number().int().positive(),
  mode: z.enum(["trusted", "restricted"]), rights: z.array(resourceRightSchema),
  grant_details: z.array(resourceRightSchema.extend({
    issuer_id: z.string(), issuer_name: z.string().optional(), created_at: z.string(), live: z.boolean(),
  })).optional(),
})
export type ResourcePolicy = z.infer<typeof resourcePolicySchema>
export type SharedResource = { kind: "routine" | "project"; id: string; name: string }
export const shareOperations = { routine: ["view", "run"], project: ["list", "read", "contribute"] } as const
export const isResourceRight = (right: z.infer<typeof resourceRightSchema>, resource: SharedResource) =>
  right.kind === resource.kind && right.id === resource.id

/** Preserve every other resource and unknown future operation; never convert accounts. */
export function resourceShareReplacement(policy: ResourcePolicy, resource: SharedResource, operations: string[], reissue: string[]) {
  if (policy.mode !== "restricted") throw new Error("Trusted access is managed through workspace roles")
  const editable = new Set<string>(shareOperations[resource.kind])
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
