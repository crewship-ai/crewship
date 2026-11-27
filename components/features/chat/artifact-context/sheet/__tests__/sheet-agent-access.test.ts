import { describe, it, expect } from "vitest"
import { agentAccessAdvice, seenVersion } from "../use-sheet-agent-access"

describe("what the server says about letting the agent change a Sheet", () => {
  // In the person's own private chat, for a Sheet uploaded there, the choice starts on.
  it("starts the choice on only when the server says so for an allowed Sheet", () => {
    expect(agentAccessAdvice({ allowed: true, default_allowed: true }, false)).toEqual({ refusal: null, handOver: false, defaultAllowed: true })
    expect(agentAccessAdvice({ allowed: true, default_allowed: "yes" }, false)).toEqual({ refusal: null, handOver: false })
    expect(agentAccessAdvice({ allowed: false, reason: "feature_off", default_allowed: true }, false).defaultAllowed).toBeUndefined()
  })
  it("allows the choice when the server allows it, also while the agent itself holds the Sheet", () => {
    expect(agentAccessAdvice({ allowed: true, max_sheets: 1 }, false)).toEqual({ refusal: null, handOver: false })
    expect(agentAccessAdvice({ allowed: true, max_sheets: 1, holder: { kind: "agent", mine: false } }, false)).toEqual({ refusal: null, handOver: false })
  })
  it("refuses with the server's own sentence", () => {
    expect(agentAccessAdvice({ allowed: false, reason: "not_attached", detail: "not_owner", message: "Only your own private Sheet.", max_sheets: 1 }, false).refusal).toBe("Only your own private Sheet.")
  })
  it("has a sentence even when the server sent none", () => {
    expect(agentAccessAdvice({ allowed: false, reason: "feature_off", max_sheets: 1 }, false).refusal).toMatch(/not turned on/)
  })
  it("lets the person's own edit go: the Sheet is handed over when the message is sent", () => {
    expect(agentAccessAdvice({ allowed: false, reason: "holder_human", holder: { kind: "human", mine: true }, max_sheets: 1 }, true)).toEqual({ refusal: null, handOver: true })
  })
  it("says to hand back in the other tab when the person edits it there", () => {
    expect(agentAccessAdvice({ allowed: false, reason: "holder_human", holder: { kind: "human", mine: true }, max_sheets: 1 }, false).refusal).toMatch(/another tab/)
  })
  it("refuses while another person edits", () => {
    expect(agentAccessAdvice({ allowed: false, reason: "holder_human", message: "Someone is editing it.", holder: { kind: "human", mine: false }, max_sheets: 1 }, false).refusal).toBe("Someone is editing it.")
  })
  it("never allows on an answer it does not understand", () => {
    expect(agentAccessAdvice({ nope: 1 }, false).refusal).toMatch(/could not be checked/)
  })
})

// The view matrix for the chip: each role sees the server's answer for it, never a rule of the client's own.
describe("who may let the agent change a Sheet, by the server's answer for each role", () => {
  const refused = (detail: string, message: string) => ({ allowed: false, reason: "not_attached", detail, message, max_sheets: 1 })
  it.each([
    ["VIEWER", refused("no_write_access", "You can only view this Sheet."), false, "You can only view this Sheet."],
    ["MEMBER who only reads", refused("no_write_access", "You can only view this Sheet."), false, "You can only view this Sheet."],
    ["MEMBER who owns the Sheet", { allowed: true, max_sheets: 1 }, false, null],
    ["MANAGER on someone else's Sheet", refused("not_owner", "Only the owner can let an agent change this Sheet."), false, "Only the owner can let an agent change this Sheet."],
    ["ADMIN on someone else's Sheet", refused("not_owner", "Only the owner can let an agent change this Sheet."), false, "Only the owner can let an agent change this Sheet."],
    ["owner in a shared chat", refused("chat_not_private", "Only in your own private chat."), false, "Only in your own private chat."],
    ["any role with the feature off", { allowed: false, reason: "feature_off", detail: "workspace_flag_off", max_sheets: 1 }, false, expect.stringMatching(/not turned on/)],
  ] as const)("%s", (_, answer, editingHere, refusal) => {
    expect(agentAccessAdvice(answer, editingHere).refusal).toEqual(refusal)
  })
  it("a role the server allows never gets a refusal the client made up, whatever the detail says", () => {
    expect(agentAccessAdvice({ allowed: true, detail: "not_owner", max_sheets: 1 }, false)).toEqual({ refusal: null, handOver: false })
  })
})

describe("the version the server names for leave without an editor open", () => {
  it("reads the head version, when and by whom it was saved", () => {
    expect(seenVersion({ allowed: true, max_sheets: 1, head_version: "12", saved_at: "2026-11-25T09:30:00Z", saved_by_name: "Dana" })).toEqual({ version: "12", savedAt: "2026-11-25T09:30:00Z", savedBy: "Dana" })
  })
  it("takes nothing that is not a positive whole number as a version", () => {
    expect(seenVersion({ allowed: true, head_version: "x" })).toBeNull()
    expect(seenVersion({ allowed: true, head_version: 12 })).toBeNull()
    expect(seenVersion({ allowed: true })).toBeNull()
  })
})
