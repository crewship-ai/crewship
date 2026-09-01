import { describe, it, expect } from "vitest"

import { SUPPORTED_AGENT_ROLES, agentRoleLabel, isLeadRole, normalizeAgentRole } from "../agent-role"

/**
 * The role presentation both crew renderers share (#2197). The behavioural
 * guard lives with those renderers
 * (components/features/crews/__tests__/agent-role-badges.test.tsx); this file
 * covers the helper directly, because it is exported and the next surface to
 * need it will call it cold.
 */

/** Tokens no create path accepts — the retired one, plus likely inventions. */
const UNSUPPORTED = ["COORDINATOR", "ORCHESTRATOR", "SUPERVISOR", "MANAGER", "WORKER"]

describe("normalizeAgentRole", () => {
  it("passes through every role the create endpoint accepts", () => {
    for (const role of SUPPORTED_AGENT_ROLES) {
      expect(normalizeAgentRole(role)).toBe(role)
    }
  })

  it("normalizes the casing and padding a model's JSON arrives in", () => {
    expect(normalizeAgentRole("lead")).toBe("LEAD")
    expect(normalizeAgentRole(" Lead ")).toBe("LEAD")
    expect(normalizeAgentRole("agent")).toBe("AGENT")
    expect(normalizeAgentRole("\tAGENT\n")).toBe("AGENT")
  })

  it("presents anything else as an ordinary agent", () => {
    for (const role of UNSUPPORTED) {
      expect(normalizeAgentRole(role)).toBe("AGENT")
      expect(normalizeAgentRole(role.toLowerCase())).toBe("AGENT")
    }
  })

  it("treats an absent role as an agent rather than a blank badge", () => {
    expect(normalizeAgentRole(null)).toBe("AGENT")
    expect(normalizeAgentRole(undefined)).toBe("AGENT")
    expect(normalizeAgentRole("")).toBe("AGENT")
    expect(normalizeAgentRole("   ")).toBe("AGENT")
  })
})

describe("agentRoleLabel / isLeadRole", () => {
  it("labels the two roles the product has", () => {
    expect(agentRoleLabel("LEAD")).toBe("Lead")
    expect(agentRoleLabel("AGENT")).toBe("Agent")
    expect(isLeadRole("LEAD")).toBe(true)
    expect(isLeadRole("AGENT")).toBe(false)
  })

  it("never labels a row with a role the product cannot create", () => {
    for (const role of UNSUPPORTED) {
      expect(agentRoleLabel(role)).toBe("Agent")
      expect(isLeadRole(role)).toBe(false)
    }
  })
})
