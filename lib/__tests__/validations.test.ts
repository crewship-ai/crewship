import { describe, it, expect } from "vitest"
import {
  createAgentSchema,
  createCrewSchema,
  updateCrewSchema,
  createCredentialSchema,
  inviteMemberSchema,
} from "@/lib/validations"
import { randomUUID } from "crypto"

describe("createAgentSchema", () => {
  const validAgent = {
    name: "Code Writer",
    slug: "code-writer",
    crew_id: randomUUID(),
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

  it("crew_id must be UUID", () => {
    const result = createAgentSchema.safeParse({
      ...validAgent,
      crew_id: "not-a-uuid",
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
    expect(result.agent_role).toBe("AGENT")
    expect(result.cli_adapter).toBe("CLAUDE_CODE")
    expect(result.temperature).toBe(0.7)
    expect(result.timeout_seconds).toBe(1800)
    expect(result.tool_profile).toBe("CODING")
  })
})

describe("createCrewSchema", () => {
  const validTeam = {
    name: "Backend Team",
    slug: "backend-team",
  }

  it("valid input passes", () => {
    const result = createCrewSchema.safeParse(validTeam)
    expect(result.success).toBe(true)
  })

  it("slug with uppercase fails", () => {
    const result = createCrewSchema.safeParse({
      ...validTeam,
      slug: "Backend-Team",
    })
    expect(result.success).toBe(false)
  })

  it("slug with special chars fails", () => {
    const result = createCrewSchema.safeParse({
      ...validTeam,
      slug: "backend_team!",
    })
    expect(result.success).toBe(false)
  })

  it("color must be #hex6", () => {
    const validColor = createCrewSchema.safeParse({
      ...validTeam,
      color: "#ff00aa",
    })
    expect(validColor.success).toBe(true)

    const invalidColor = createCrewSchema.safeParse({
      ...validTeam,
      color: "red",
    })
    expect(invalidColor.success).toBe(false)

    const shortHex = createCrewSchema.safeParse({
      ...validTeam,
      color: "#fff",
    })
    expect(shortHex.success).toBe(false)
  })

  it("icon max 10 chars", () => {
    const validIcon = createCrewSchema.safeParse({
      ...validTeam,
      icon: "🚀",
    })
    expect(validIcon.success).toBe(true)

    const tooLong = createCrewSchema.safeParse({
      ...validTeam,
      icon: "a".repeat(11),
    })
    expect(tooLong.success).toBe(false)
  })

  it("container_memory_mb must be 512-32768", () => {
    const tooLow = createCrewSchema.safeParse({ ...validTeam, container_memory_mb: 256 })
    expect(tooLow.success).toBe(false)

    const tooHigh = createCrewSchema.safeParse({ ...validTeam, container_memory_mb: 65536 })
    expect(tooHigh.success).toBe(false)

    const valid = createCrewSchema.safeParse({ ...validTeam, container_memory_mb: 2048 })
    expect(valid.success).toBe(true)
  })

  it("container_cpus must be 0.5-16", () => {
    const tooLow = createCrewSchema.safeParse({ ...validTeam, container_cpus: 0.25 })
    expect(tooLow.success).toBe(false)

    const tooHigh = createCrewSchema.safeParse({ ...validTeam, container_cpus: 32 })
    expect(tooHigh.success).toBe(false)

    const valid = createCrewSchema.safeParse({ ...validTeam, container_cpus: 2 })
    expect(valid.success).toBe(true)
  })

  it("container_ttl_hours must be 1-720 or null", () => {
    const tooLow = createCrewSchema.safeParse({ ...validTeam, container_ttl_hours: 0 })
    expect(tooLow.success).toBe(false)

    const tooHigh = createCrewSchema.safeParse({ ...validTeam, container_ttl_hours: 721 })
    expect(tooHigh.success).toBe(false)

    const valid = createCrewSchema.safeParse({ ...validTeam, container_ttl_hours: 24 })
    expect(valid.success).toBe(true)

    const nullValue = createCrewSchema.safeParse({ ...validTeam, container_ttl_hours: null })
    expect(nullValue.success).toBe(true)
  })
})

describe("updateCrewSchema", () => {
  it("allows partial updates (empty object)", () => {
    const result = updateCrewSchema.safeParse({})
    expect(result.success).toBe(true)
  })

  it("allows updating only name", () => {
    const result = updateCrewSchema.safeParse({ name: "New Name" })
    expect(result.success).toBe(true)
  })

  it("allows updating only container config", () => {
    const result = updateCrewSchema.safeParse({
      container_memory_mb: 4096,
      container_cpus: 4,
      container_ttl_hours: 48,
    })
    expect(result.success).toBe(true)
  })

  it("still validates field constraints on partial update", () => {
    const badMemory = updateCrewSchema.safeParse({ container_memory_mb: 100 })
    expect(badMemory.success).toBe(false)

    const badCpus = updateCrewSchema.safeParse({ container_cpus: 0.1 })
    expect(badCpus.success).toBe(false)

    const badName = updateCrewSchema.safeParse({ name: "A" })
    expect(badName.success).toBe(false)
  })
})

describe("createCredentialSchema", () => {
  it("valid workspace scope (no crew_id)", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "WORKSPACE",
    })
    expect(result.success).toBe(true)
  })

  it("valid crew scope (with crew_id)", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "CREW",
      crew_id: randomUUID(),
    })
    expect(result.success).toBe(true)
  })

  it("crew scope without crew_id fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "CREW",
    })
    expect(result.success).toBe(false)
  })

  it("workspace scope with crew_id fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "sk-1234567890",
      scope: "WORKSPACE",
      crew_id: randomUUID(),
    })
    expect(result.success).toBe(false)
  })

  it("empty value fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "",
      scope: "WORKSPACE",
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
