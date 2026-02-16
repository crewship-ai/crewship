import { describe, it, expect } from "vitest"
import {
  createAgentSchema,
  createTeamSchema,
  createCredentialSchema,
  inviteMemberSchema,
} from "@/lib/validations"
import { randomUUID } from "crypto"

describe("createAgentSchema", () => {
  const validAgent = {
    name: "Code Writer",
    slug: "code-writer",
    team_id: randomUUID(),
  }

  it("valid input passes", () => {
    const result = createAgentSchema.safeParse(validAgent)
    expect(result.success).toBe(true)
  })

  it("missing name fails", () => {
    const result = createAgentSchema.safeParse({
      ...validAgent,
      name: undefined,
    })
    expect(result.success).toBe(false)
  })

  it("slug with uppercase fails", () => {
    const result = createAgentSchema.safeParse({
      ...validAgent,
      slug: "Code-Writer",
    })
    expect(result.success).toBe(false)
  })

  it("slug with spaces fails", () => {
    const result = createAgentSchema.safeParse({
      ...validAgent,
      slug: "code writer",
    })
    expect(result.success).toBe(false)
  })

  it("team_id must be UUID", () => {
    const result = createAgentSchema.safeParse({
      ...validAgent,
      team_id: "not-a-uuid",
    })
    expect(result.success).toBe(false)
  })

  it("temperature must be 0-2", () => {
    const tooLow = createAgentSchema.safeParse({
      ...validAgent,
      temperature: -0.1,
    })
    expect(tooLow.success).toBe(false)

    const tooHigh = createAgentSchema.safeParse({
      ...validAgent,
      temperature: 2.1,
    })
    expect(tooHigh.success).toBe(false)

    const valid = createAgentSchema.safeParse({
      ...validAgent,
      temperature: 1.5,
    })
    expect(valid.success).toBe(true)
  })

  it("timeout_seconds must be 30-7200", () => {
    const tooLow = createAgentSchema.safeParse({
      ...validAgent,
      timeout_seconds: 29,
    })
    expect(tooLow.success).toBe(false)

    const tooHigh = createAgentSchema.safeParse({
      ...validAgent,
      timeout_seconds: 7201,
    })
    expect(tooHigh.success).toBe(false)

    const valid = createAgentSchema.safeParse({
      ...validAgent,
      timeout_seconds: 3600,
    })
    expect(valid.success).toBe(true)
  })

  it("applies correct default values", () => {
    const result = createAgentSchema.parse(validAgent)
    expect(result.agent_role).toBe("WORKER")
    expect(result.cli_adapter).toBe("CLAUDE_CODE")
    expect(result.temperature).toBe(0.7)
    expect(result.timeout_seconds).toBe(1800)
    expect(result.tool_profile).toBe("CODING")
  })
})

describe("createTeamSchema", () => {
  const validTeam = {
    name: "Backend Team",
    slug: "backend-team",
  }

  it("valid input passes", () => {
    const result = createTeamSchema.safeParse(validTeam)
    expect(result.success).toBe(true)
  })

  it("slug with uppercase fails", () => {
    const result = createTeamSchema.safeParse({
      ...validTeam,
      slug: "Backend-Team",
    })
    expect(result.success).toBe(false)
  })

  it("slug with special chars fails", () => {
    const result = createTeamSchema.safeParse({
      ...validTeam,
      slug: "backend_team!",
    })
    expect(result.success).toBe(false)
  })

  it("color must be #hex6", () => {
    const validColor = createTeamSchema.safeParse({
      ...validTeam,
      color: "#ff00aa",
    })
    expect(validColor.success).toBe(true)

    const invalidColor = createTeamSchema.safeParse({
      ...validTeam,
      color: "red",
    })
    expect(invalidColor.success).toBe(false)

    const shortHex = createTeamSchema.safeParse({
      ...validTeam,
      color: "#fff",
    })
    expect(shortHex.success).toBe(false)
  })

  it("icon max 10 chars", () => {
    const validIcon = createTeamSchema.safeParse({
      ...validTeam,
      icon: "🚀",
    })
    expect(validIcon.success).toBe(true)

    const tooLong = createTeamSchema.safeParse({
      ...validTeam,
      icon: "a".repeat(11),
    })
    expect(tooLong.success).toBe(false)
  })
})

describe("createCredentialSchema", () => {
  it("valid org scope (no team_id)", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "ORGANIZATION",
    })
    expect(result.success).toBe(true)
  })

  it("valid team scope (with team_id)", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "TEAM",
      team_id: randomUUID(),
    })
    expect(result.success).toBe(true)
  })

  it("team scope without team_id fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "TEAM",
    })
    expect(result.success).toBe(false)
  })

  it("org scope with team_id fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "ORGANIZATION",
      team_id: randomUUID(),
    })
    expect(result.success).toBe(false)
  })

  it("empty value fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "",
      scope: "ORGANIZATION",
    })
    expect(result.success).toBe(false)
  })
})

describe("inviteMemberSchema", () => {
  it("valid email passes", () => {
    const result = inviteMemberSchema.safeParse({
      email: "user@example.com",
      role: "MEMBER",
    })
    expect(result.success).toBe(true)
  })

  it("invalid email fails", () => {
    const result = inviteMemberSchema.safeParse({
      email: "not-an-email",
      role: "MEMBER",
    })
    expect(result.success).toBe(false)
  })

  it("role must be one of ADMIN/MANAGER/MEMBER/VIEWER", () => {
    const validRoles = ["ADMIN", "MANAGER", "MEMBER", "VIEWER"]
    for (const role of validRoles) {
      const result = inviteMemberSchema.safeParse({
        email: "user@example.com",
        role,
      })
      expect(result.success).toBe(true)
    }
  })

  it("OWNER is not a valid invite role", () => {
    const result = inviteMemberSchema.safeParse({
      email: "user@example.com",
      role: "OWNER",
    })
    expect(result.success).toBe(false)
  })

  it("defaults role to MEMBER", () => {
    const result = inviteMemberSchema.parse({
      email: "user@example.com",
    })
    expect(result.role).toBe("MEMBER")
  })
})
