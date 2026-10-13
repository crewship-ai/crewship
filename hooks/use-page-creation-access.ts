"use client"

import { deriveAccessMode } from "@/hooks/use-access-mode"
import { useWorkspace } from "@/hooks/use-workspace"
import { Capability, hasCapability } from "@/lib/capabilities"

/** Mirrors the existing Pages create/import role-or-capability admission.
 * This is an affordance, not authority: the server rechecks every write. */
export function usePageCreationAccess(workspaceId: string): boolean {
  const { workspaceId: selectedId, workspaces, role, capabilities, loading, error } = useWorkspace()
  if (loading || error || selectedId !== workspaceId) return false
  if (deriveAccessMode(workspaces, loading, error) !== "trusted") return false
  return role === "OWNER" || role === "ADMIN" || role === "MANAGER" || hasCapability(capabilities, Capability.PageCreate)
}
