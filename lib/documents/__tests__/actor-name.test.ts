import { describe, expect, it } from "vitest"
import { documentActorName } from "../actor-name"

const members = [
  { user: { id: "u-named", full_name: "Demo User", email: "demo@example.com" } },
  { user: { id: "u-mail", full_name: "", email: "mail@example.com" } },
]

describe("documentActorName", () => {
  it.each([
    ["user:u-self", "You"],
    ["user:u-named", "Demo User"],
    ["user:u-mail", "mail@example.com"],
    ["user:u-gone", "Workspace member"],
    ["agent:a-1", "Agent"],
    ["human:legacy", "Workspace member"],
    ["", "Unknown author"],
    ["system", "Unknown author"],
  ])("%s → %s", (actor, expected) => {
    expect(documentActorName(actor, { humanId: "u-self", members })).toBe(expected)
  })
  it("never returns the raw identifier", () => {
    for (const actor of ["user:cmw86cvse000134809d57", "agent:cmw1", "crew:cmw2", "cmw3"]) {
      expect(documentActorName(actor, { humanId: "x", members: [] })).not.toMatch(/cmw/)
    }
  })
})
