import { describe, it, expect } from "vitest"
import {
  createAgentSchema,
  updateAgentSchema,
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

  it("crew_id must be non-empty string", () => {
    const result = createAgentSchema.safeParse({
      ...validAgent,
      crew_id: "",
    })
    expect(result.success).toBe(false)
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
    expect(result.timeout_seconds).toBe(1800)
    expect(result.tool_profile).toBe("CODING")
  })

  // Role ↔ crew_id conditional validation — mirrors backend rules in
  // internal/api/agents.go (LEAD requires crew_id, AGENT requires one
  // by convention). COORDINATOR was retired in v0.1.
  describe("role-based crew_id rules", () => {
    const base = { name: "Code Writer", slug: "code-writer" }

    it("AGENT with crew_id passes", () => {
      const result = createAgentSchema.safeParse({
        ...base,
        agent_role: "AGENT",
        crew_id: randomUUID(),
      })
      expect(result.success).toBe(true)
    })

    it("AGENT without crew_id fails", () => {
      const result = createAgentSchema.safeParse({
        ...base,
        agent_role: "AGENT",
      })
      expect(result.success).toBe(false)
    })

    it("LEAD with crew_id passes", () => {
      const result = createAgentSchema.safeParse({
        ...base,
        agent_role: "LEAD",
        crew_id: randomUUID(),
      })
      expect(result.success).toBe(true)
    })

    it("LEAD without crew_id fails", () => {
      const result = createAgentSchema.safeParse({
        ...base,
        agent_role: "LEAD",
      })
      expect(result.success).toBe(false)
    })

    it("COORDINATOR rejected (retired in v0.1)", () => {
      const result = createAgentSchema.safeParse({
        ...base,
        agent_role: "COORDINATOR",
      })
      expect(result.success).toBe(false)
    })
  })
})

describe("updateAgentSchema", () => {
  it("empty object is a valid partial update", () => {
    const result = updateAgentSchema.safeParse({})
    expect(result.success).toBe(true)
  })

  it("updating only name passes", () => {
    const result = updateAgentSchema.safeParse({ name: "Renamed" })
    expect(result.success).toBe(true)
  })

  it("changing role to LEAD without crew_id fails", () => {
    const result = updateAgentSchema.safeParse({ agent_role: "LEAD" })
    expect(result.success).toBe(false)
  })

  it("changing role to COORDINATOR rejected (retired in v0.1)", () => {
    const result = updateAgentSchema.safeParse({ agent_role: "COORDINATOR" })
    expect(result.success).toBe(false)
  })
})

describe("createCrewSchema", () => {
  const validCrew = {
    name: "Backend Team",
    slug: "backend-team",
  }

  it("valid input passes", () => {
    const result = createCrewSchema.safeParse(validCrew)
    expect(result.success).toBe(true)
  })

  it("slug with uppercase fails", () => {
    const result = createCrewSchema.safeParse({
      ...validCrew,
      slug: "Backend-Team",
    })
    expect(result.success).toBe(false)
  })

  it("slug with special chars fails", () => {
    const result = createCrewSchema.safeParse({
      ...validCrew,
      slug: "backend_team!",
    })
    expect(result.success).toBe(false)
  })

  it("color must be #hex6", () => {
    const validColor = createCrewSchema.safeParse({
      ...validCrew,
      color: "#ff00aa",
    })
    expect(validColor.success).toBe(true)

    const invalidColor = createCrewSchema.safeParse({
      ...validCrew,
      color: "red",
    })
    expect(invalidColor.success).toBe(false)

    const shortHex = createCrewSchema.safeParse({
      ...validCrew,
      color: "#fff",
    })
    expect(shortHex.success).toBe(false)
  })

  it("icon max 10 chars", () => {
    const validIcon = createCrewSchema.safeParse({
      ...validCrew,
      icon: "🚀",
    })
    expect(validIcon.success).toBe(true)

    const tooLong = createCrewSchema.safeParse({
      ...validCrew,
      icon: "a".repeat(11),
    })
    expect(tooLong.success).toBe(false)
  })

  it("container_memory_mb must be 512-32768", () => {
    const tooLow = createCrewSchema.safeParse({ ...validCrew, container_memory_mb: 256 })
    expect(tooLow.success).toBe(false)

    const tooHigh = createCrewSchema.safeParse({ ...validCrew, container_memory_mb: 65536 })
    expect(tooHigh.success).toBe(false)

    const valid = createCrewSchema.safeParse({ ...validCrew, container_memory_mb: 2048 })
    expect(valid.success).toBe(true)
  })

  it("container_cpus must be 0.5-16", () => {
    const tooLow = createCrewSchema.safeParse({ ...validCrew, container_cpus: 0.25 })
    expect(tooLow.success).toBe(false)

    const tooHigh = createCrewSchema.safeParse({ ...validCrew, container_cpus: 32 })
    expect(tooHigh.success).toBe(false)

    const valid = createCrewSchema.safeParse({ ...validCrew, container_cpus: 2 })
    expect(valid.success).toBe(true)
  })

  it("container_ttl_hours must be 1-720 or null", () => {
    const tooLow = createCrewSchema.safeParse({ ...validCrew, container_ttl_hours: 0 })
    expect(tooLow.success).toBe(false)

    const tooHigh = createCrewSchema.safeParse({ ...validCrew, container_ttl_hours: 721 })
    expect(tooHigh.success).toBe(false)

    const valid = createCrewSchema.safeParse({ ...validCrew, container_ttl_hours: 24 })
    expect(valid.success).toBe(true)

    const nullValue = createCrewSchema.safeParse({ ...validCrew, container_ttl_hours: null })
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
      value: "test-token-value",
      scope: "WORKSPACE",
    })
    expect(result.success).toBe(true)
  })

  it("valid crew scope (with crew_id)", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "test-token-value",
      scope: "CREW",
      crew_id: randomUUID(),
    })
    expect(result.success).toBe(true)
  })

  it("crew scope without crew_id fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "test-token-value",
      scope: "CREW",
    })
    expect(result.success).toBe(false)
  })

  it("workspace scope with crew_id fails", () => {
    const result = createCredentialSchema.safeParse({
      name: "OPENAI_API_KEY",
      value: "test-token-value",
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
