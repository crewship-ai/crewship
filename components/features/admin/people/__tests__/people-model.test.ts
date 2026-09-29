import { describe, it, expect } from "vitest"
import {
  facetMatches, isLastOwner, personMatches, personStatus, byAttentionThenName, daysUntil, type Person,
} from "../people-model"

const NOW = Date.parse("2026-09-29T12:00:00Z")
const person = (over: Partial<Person> = {}): Person => ({
  id: "u1", email: "jana@acme.example", full_name: "Jana", created_at: "2026-09-01T00:00:00Z",
  workspace: null, role: null, memberships: [], ...over,
})
const member = (workspace_id: string, role: string) => ({ workspace_id, name: workspace_id, slug: workspace_id, role, joined_at: "2026-09-01" })

describe("personStatus", () => {
  it("names the most pressing state: suspended, then locked, then a pending setup", () => {
    const all = { suspended_at: "2026-09-28T00:00:00Z", locked_until: "2026-09-29T13:00:00Z", setup_link_expires_at: "2026-10-01T00:00:00Z" }
    expect(personStatus(person(all), NOW)).toBe("suspended")
    expect(personStatus(person({ ...all, suspended_at: null }), NOW)).toBe("locked")
    expect(personStatus(person({ setup_link_expires_at: "2026-10-01T00:00:00Z" }), NOW)).toBe("setup")
  })
  it("treats an expired lock or link as nothing", () => {
    expect(personStatus(person({ locked_until: "2026-09-29T11:00:00Z", setup_link_expires_at: "2026-09-20T00:00:00Z" }), NOW)).toBe("active")
  })
})

describe("facets", () => {
  const people = [
    person({ id: "a", locked_until: "2026-09-29T13:00:00Z", memberships: [member("w1", "MEMBER")] }),
    person({ id: "b", setup_link_expires_at: "2026-10-01T00:00:00Z" }),
    person({ id: "c", memberships: [member("w1", "OWNER")], instance_admin: true }),
  ]
  const count = (f: Parameters<typeof facetMatches>[1]) => people.filter((p) => facetMatches(p, f, NOW)).map((p) => p.id)
  it("counts what each facet says", () => {
    expect(count("attention")).toEqual(["a", "b"])
    expect(count("noaccess")).toEqual(["b"])
    expect(count("admins")).toEqual(["c"])
    expect(count("all")).toHaveLength(3)
  })
  it("puts people needing attention first", () => {
    expect([...people].reverse().sort((x, y) => byAttentionThenName(x, y, NOW)).map((p) => p.id).slice(0, 2).sort()).toEqual(["a", "b"])
  })
})

describe("isLastOwner", () => {
  it("guards only the one owner a workspace has left", () => {
    const one = [person({ id: "o", memberships: [member("w", "OWNER")] }), person({ id: "m", memberships: [member("w", "ADMIN")] })]
    expect(isLastOwner(one, "w", "o")).toBe(true)
    expect(isLastOwner(one, "w", "m")).toBe(false)
    const two = [...one, person({ id: "o2", memberships: [member("w", "OWNER")] })]
    expect(isLastOwner(two, "w", "o")).toBe(false)
  })
})

describe("search and dates", () => {
  it("finds a person by name, email or workspace", () => {
    const p = person({ memberships: [member("Research Lab", "MEMBER")] })
    expect(personMatches(p, "research")).toBe(true)
    expect(personMatches(p, "ACME")).toBe(true)
    expect(personMatches(p, "nobody")).toBe(false)
  })
  it("rounds days until a link expires up, and never below zero", () => {
    expect(daysUntil("2026-09-30T00:00:00Z", NOW)).toBe(1)
    expect(daysUntil("2026-09-01T00:00:00Z", NOW)).toBe(0)
  })
})
