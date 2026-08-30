import type { AgentRole } from "./agent-personas"

/**
 * Presenting an `agent_role` that arrived over the wire.
 *
 * `AGENT` and `LEAD` are the only values POST /api/v1/agents accepts
 * (`validAgentRoles`, internal/api/agents.go). Anything else is either the
 * COORDINATOR retired in v0.1 or a token a language model invented for the
 * crew designer, and a UI that prints it raw promises a role the product
 * cannot create — the wizard rendered a Coordinator in its lineup preview and
 * then failed the creation it had just offered (#2197).
 *
 * The server-side fix is the load-bearing one; these helpers exist because
 * every one of these values crosses a network boundary, where a TypeScript
 * union is a claim rather than a guarantee. Stored rows predating the
 * retirement reach the roster the same way.
 */

/** What the create endpoint accepts. Keep in step with `validAgentRoles`. */
export const SUPPORTED_AGENT_ROLES: readonly AgentRole[] = ["AGENT", "LEAD"]

/**
 * The role to present for a token of unknown provenance.
 *
 * Unsupported roles fall back to `AGENT` rather than being hidden or shown
 * raw: an ordinary agent is the one thing the create form can actually
 * deliver for that row, so the fallback describes something true.
 */
export function normalizeAgentRole(role: string | null | undefined): AgentRole {
  const token = (role ?? "").trim().toUpperCase()
  return (SUPPORTED_AGENT_ROLES as readonly string[]).includes(token) ? (token as AgentRole) : "AGENT"
}

/** Human-readable label for a role badge. */
export function agentRoleLabel(role: string | null | undefined): string {
  return normalizeAgentRole(role) === "LEAD" ? "Lead" : "Agent"
}

/** Whether a role earns a badge at all — only a lead stands out from the crew. */
export function isLeadRole(role: string | null | undefined): boolean {
  return normalizeAgentRole(role) === "LEAD"
}
