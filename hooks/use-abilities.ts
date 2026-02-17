"use client"

import { useMemo } from "react"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { AppAbility } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"
import { useWorkspace } from "@/hooks/use-workspace"

interface UseAbilitiesReturn {
  abilities: AppAbility
  role: string | null
  loading: boolean
}

/**
 * Returns CASL abilities for the current user's workspace role.
 * Use `abilities.can("create", "Agent")` to conditionally render UI.
 */
export function useAbilities(): UseAbilitiesReturn {
  const { role, loading } = useWorkspace()

  const abilities = useMemo(() => {
    if (!role) return defineAbilitiesFor("VIEWER" as OrgRole)
    return defineAbilitiesFor(role as OrgRole)
  }, [role])

  return { abilities, role, loading }
}
