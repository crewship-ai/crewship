import { describe, it, expect, vi } from "vitest"

// Mock the Prisma generated client to avoid dependency on generated files
vi.mock("@/lib/generated/prisma/client", () => ({
  OrgRole: {
    OWNER: "OWNER",
    ADMIN: "ADMIN",
    MANAGER: "MANAGER",
    MEMBER: "MEMBER",
    VIEWER: "VIEWER",
  },
}))

import { defineAbilitiesFor } from "@/lib/permissions/abilities"

type OrgRole = "OWNER" | "ADMIN" | "MANAGER" | "MEMBER" | "VIEWER"

describe("defineAbilitiesFor", () => {
  describe("OWNER", () => {
    const ability = defineAbilitiesFor("OWNER" as OrgRole)

    it("can manage all", () => {
      expect(ability.can("manage", "all")).toBe(true)
    })

    it("can create, read, update, delete any subject", () => {
      const subjects = [
        "Workspace",
        "Crew",
        "Agent",
        "Credential",
        "Skill",
        "AuditLog",
        "Member",
      ] as const
      for (const subject of subjects) {
        expect(ability.can("create", subject)).toBe(true)
        expect(ability.can("read", subject)).toBe(true)
        expect(ability.can("update", subject)).toBe(true)
        expect(ability.can("delete", subject)).toBe(true)
      }
    })
  })

  describe("ADMIN", () => {
    const ability = defineAbilitiesFor("ADMIN" as OrgRole)

    it("can manage all", () => {
      expect(ability.can("manage", "all")).toBe(true)
    })

    it("can create, read, update, delete any subject", () => {
      const subjects = [
        "Workspace",
        "Crew",
        "Agent",
        "Credential",
        "Skill",
        "AuditLog",
        "Member",
      ] as const
      for (const subject of subjects) {
        expect(ability.can("create", subject)).toBe(true)
        expect(ability.can("read", subject)).toBe(true)
        expect(ability.can("update", subject)).toBe(true)
        expect(ability.can("delete", subject)).toBe(true)
      }
    })
  })

  describe("MANAGER", () => {
    const ability = defineAbilitiesFor("MANAGER" as OrgRole)

    it("can create Team, Agent, Credential", () => {
      expect(ability.can("create", "Crew")).toBe(true)
      expect(ability.can("create", "Agent")).toBe(true)
      expect(ability.can("create", "Credential")).toBe(true)
    })

    it("can read Team, Agent, Credential, Skill, AuditLog, Member", () => {
      expect(ability.can("read", "Crew")).toBe(true)
      expect(ability.can("read", "Agent")).toBe(true)
      expect(ability.can("read", "Credential")).toBe(true)
      expect(ability.can("read", "Skill")).toBe(true)
      expect(ability.can("read", "AuditLog")).toBe(true)
      expect(ability.can("read", "Member")).toBe(true)
    })

    it("can update Team, Agent, Credential", () => {
      expect(ability.can("update", "Crew")).toBe(true)
      expect(ability.can("update", "Agent")).toBe(true)
      expect(ability.can("update", "Credential")).toBe(true)
    })

    it("cannot delete Team, Agent, Credential", () => {
      expect(ability.can("delete", "Crew")).toBe(false)
      expect(ability.can("delete", "Agent")).toBe(false)
      expect(ability.can("delete", "Credential")).toBe(false)
    })

    it("cannot manage Workspace", () => {
      expect(ability.can("create", "Workspace")).toBe(false)
      expect(ability.can("update", "Workspace")).toBe(false)
      expect(ability.can("delete", "Workspace")).toBe(false)
    })

    it("cannot manage Member", () => {
      expect(ability.can("create", "Member")).toBe(false)
      expect(ability.can("update", "Member")).toBe(false)
      expect(ability.can("delete", "Member")).toBe(false)
    })
  })

  describe("MEMBER", () => {
    const ability = defineAbilitiesFor("MEMBER" as OrgRole)

    it("can read Org, Team, Agent, Skill", () => {
      expect(ability.can("read", "Workspace")).toBe(true)
      expect(ability.can("read", "Crew")).toBe(true)
      expect(ability.can("read", "Agent")).toBe(true)
      expect(ability.can("read", "Skill")).toBe(true)
    })

    it("cannot create anything", () => {
      expect(ability.can("create", "Workspace")).toBe(false)
      expect(ability.can("create", "Crew")).toBe(false)
      expect(ability.can("create", "Agent")).toBe(false)
      expect(ability.can("create", "Credential")).toBe(false)
    })

    it("cannot update anything", () => {
      expect(ability.can("update", "Workspace")).toBe(false)
      expect(ability.can("update", "Crew")).toBe(false)
      expect(ability.can("update", "Agent")).toBe(false)
      expect(ability.can("update", "Credential")).toBe(false)
    })

    it("cannot delete anything", () => {
      expect(ability.can("delete", "Workspace")).toBe(false)
      expect(ability.can("delete", "Crew")).toBe(false)
      expect(ability.can("delete", "Agent")).toBe(false)
      expect(ability.can("delete", "Credential")).toBe(false)
    })

    it("cannot read Credential, AuditLog, Member", () => {
      expect(ability.can("read", "Credential")).toBe(false)
      expect(ability.can("read", "AuditLog")).toBe(false)
      expect(ability.can("read", "Member")).toBe(false)
    })
  })

  describe("VIEWER", () => {
    const ability = defineAbilitiesFor("VIEWER" as OrgRole)

    it("can read Org, Team, Agent, Skill", () => {
      expect(ability.can("read", "Workspace")).toBe(true)
      expect(ability.can("read", "Crew")).toBe(true)
      expect(ability.can("read", "Agent")).toBe(true)
      expect(ability.can("read", "Skill")).toBe(true)
    })

    it("cannot create anything", () => {
      expect(ability.can("create", "Workspace")).toBe(false)
      expect(ability.can("create", "Crew")).toBe(false)
      expect(ability.can("create", "Agent")).toBe(false)
      expect(ability.can("create", "Credential")).toBe(false)
    })

    it("cannot update anything", () => {
      expect(ability.can("update", "Workspace")).toBe(false)
      expect(ability.can("update", "Crew")).toBe(false)
      expect(ability.can("update", "Agent")).toBe(false)
      expect(ability.can("update", "Credential")).toBe(false)
    })

    it("cannot delete anything", () => {
      expect(ability.can("delete", "Workspace")).toBe(false)
      expect(ability.can("delete", "Crew")).toBe(false)
      expect(ability.can("delete", "Agent")).toBe(false)
      expect(ability.can("delete", "Credential")).toBe(false)
    })

    it("cannot read Credential, AuditLog, Member", () => {
      expect(ability.can("read", "Credential")).toBe(false)
      expect(ability.can("read", "AuditLog")).toBe(false)
      expect(ability.can("read", "Member")).toBe(false)
    })
  })
})
