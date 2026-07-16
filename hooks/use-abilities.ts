"use client"

import { useCallback, useMemo } from "react"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { AppAbility } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"
import { hasCapability as capSetHas } from "@/lib/capabilities"
import type { CapabilityValue } from "@/lib/capabilities"
import { useWorkspace } from "@/hooks/use-workspace"

interface UseAbilitiesReturn {
  abilities: AppAbility
  role: string | null
  /** Raw per-membership capability grants (#1034); null when unknown. */
  capabilities: string[] | null
  /** True when the caller's membership grants the capability. Layered
   *  on top of the CASL role abilities — mirrors the backend's
   *  requireRoleOrCapabilityOrForbid, so gate UI with
   *  `can(...) || hasCapability(...)` for role-OR-capability routes. */
  hasCapability: (capability: CapabilityValue) => boolean
  loading: boolean
}

/**
 * Returns CASL abilities for the current user's workspace role, plus the
 * per-membership capability grants exposed by the workspaces API.
 * Use `abilities.can("create", "Agent")` to conditionally render UI;
 * use `hasCapability(Capability.CredentialRotate)` where the backend
 * honors a capability for lower roles.
 */
export function useAbilities(): UseAbilitiesReturn {
  const { role, capabilities, loading } = useWorkspace()

  const abilities = useMemo(() => {
    if (!role) return defineAbilitiesFor("VIEWER" as OrgRole)
    return defineAbilitiesFor(role as OrgRole)
  }, [role])

  const hasCapability = useCallback(
    (capability: CapabilityValue) => capSetHas(capabilities, capability),
    [capabilities],
  )

  return { abilities, role, capabilities: capabilities ?? null, hasCapability, loading }
}
