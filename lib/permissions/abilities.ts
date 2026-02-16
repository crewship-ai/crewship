import { AbilityBuilder, PureAbility } from "@casl/ability"
import type { OrgRole } from "@/lib/generated/prisma/client"

type Actions = "create" | "read" | "update" | "delete" | "manage"
type Subjects =
  | "Organization"
  | "Team"
  | "Agent"
  | "Credential"
  | "Skill"
  | "AuditLog"
  | "Member"
  | "all"

export type AppAbility = PureAbility<[Actions, Subjects]>

export function defineAbilitiesFor(role: OrgRole): AppAbility {
  const { can, build } = new AbilityBuilder<AppAbility>(PureAbility)

  switch (role) {
    case "OWNER":
    case "ADMIN":
      can("manage", "all")
      break

    case "MANAGER":
      can("read", "Organization")
      can("create", "Team")
      can("read", "Team")
      can("update", "Team")
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
      can("read", "Organization")
      can("read", "Team")
      can("read", "Agent")
      can("read", "Skill")
      break

    case "VIEWER":
      can("read", "Organization")
      can("read", "Team")
      can("read", "Agent")
      can("read", "Skill")
      break
  }

  return build()
}
