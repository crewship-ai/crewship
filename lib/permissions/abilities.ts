import { AbilityBuilder, Ability } from "@casl/ability"
import type { OrgRole } from "@/lib/generated/prisma/client"

type Actions = "create" | "read" | "update" | "delete" | "manage"
type Subjects =
  | "Workspace"
  | "Crew"
  | "Agent"
  | "Credential"
  | "Skill"
  | "AuditLog"
  | "Member"
  | "all"

/** CASL ability type parameterized with Crewship actions and subjects. */
export type AppAbility = Ability<[Actions, Subjects]>

/**
 * Build a CASL ability set for a given workspace role.
 * OWNER/ADMIN can manage everything; MANAGER can CRUD crews/agents/credentials;
 * MEMBER/VIEWER have read-only access.
 */
export function defineAbilitiesFor(role: OrgRole): AppAbility {
  const { can, build } = new AbilityBuilder<AppAbility>(Ability)

  switch (role) {
    case "OWNER":
    case "ADMIN":
      can("manage", "all")
      break

    case "MANAGER":
      can("read", "Workspace")
      can("create", "Crew")
      can("read", "Crew")
      can("update", "Crew")
      can("create", "Agent")
      can("read", "Agent")
      can("update", "Agent")
      can("read", "Credential")
      can("create", "Credential")
      can("update", "Credential")
      can("read", "Skill")
      can("read", "AuditLog")
      can("read", "Member")
      break

    case "MEMBER":
      can("read", "Workspace")
      can("read", "Crew")
      can("read", "Agent")
      can("read", "Skill")
      // Members see credential metadata (names, providers, statuses)
      // for credentials in their crews — values are never exposed by
      // the API. Mirrors the BE List/Get which only enforces workspace
      // membership, plus the new crew-scoped visibility filter.
      can("read", "Credential")
      break

    case "VIEWER":
      can("read", "Workspace")
      can("read", "Crew")
      can("read", "Agent")
      can("read", "Skill")
      can("read", "Credential")
      break
  }

  return build()
}
