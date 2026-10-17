import { describe, expect, it } from "vitest"
import { accessChange, accessSummary, accountRows, filterAccounts } from "../tools-model"
import type { AgentLite, ConnectedAccount } from "../composio/types"

const gh: ConnectedAccount = { id: "ca_gh", user_id: "u1", status: "ACTIVE", toolkit: { slug: "github" } }
const lin: ConnectedAccount = { id: "ca_lin", user_id: "u1", status: "EXPIRED", toolkit: { slug: "linear" } }
const a1: AgentLite = { id: "a1", name: "Reviewer", slug: "reviewer", crew: { name: "Code review" } }
const a2: AgentLite = { id: "a2", name: "Fixer", slug: "fixer", crew: { name: "Code review" } }
const a3: AgentLite = { id: "a3", name: "Scout", slug: "scout", crew: { name: "Research" } }
const bindings = {
  a1: [{ toolkit: "github", mode: "full" as const, user_id: "u1", endpoint: "" }],
  a2: [{ toolkit: "github", mode: "full" as const, user_id: "u1", endpoint: "" }, { toolkit: "notion", mode: "custom" as const, user_id: "u1", endpoint: "", tools: ["NOTION_GET_PAGE"] }],
  a3: [{ toolkit: "github", mode: "read" as const, user_id: "u1", endpoint: "" }],
}

describe("accountRows", () => {
  it("groups the agents holding an app by crew", () => {
    const rows = accountRows([gh, lin], [a1, a2, a3], bindings)
    expect(accessSummary(rows[0])).toBe("Code review: 2 agents Full · Research: 1 agent Read-only")
    expect(accessSummary(rows[1])).toBe("Not given to any agent")
    expect(rows[1].signedOut).toBe(true)
    expect(filterAccounts(rows, "attention", "", null).map((r) => r.slug)).toEqual(["linear"])
    expect(filterAccounts(rows, "idle", "", null).map((r) => r.slug)).toEqual(["linear"])
  })
})

describe("accessChange", () => {
  it("off unbinds just this app, never posts an empty list", () => {
    expect(accessChange(bindings.a1, "github", "off", "u1")).toEqual({ kind: "unbind", toolkit: "github" })
  })

  it("custom keeps tool list while another app changes", () => {
    expect(accessChange(bindings.a2, "github", "read", "u1")).toEqual({
      kind: "bind",
      payload: { user_id: "u1", apps: [{ toolkit: "notion", mode: "custom", tools: ["NOTION_GET_PAGE"] }, { toolkit: "github", mode: "read" }] },
    })
  })

  it("adds an app for an agent without one, as this account's user", () => {
    expect(accessChange([], "linear", "full", "u1")).toEqual({ kind: "bind", payload: { user_id: "u1", apps: [{ toolkit: "linear", mode: "full" }] } })
  })

  it("refuses when the agent acts as another Composio user", () => {
    expect(accessChange(bindings.a1, "linear", "read", "u2")).toBeNull()
  })
})
