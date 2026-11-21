import { describe, it, expect } from "vitest"
import {
  isPersonal, workspaceMatches, facetMatches, isLastOwner, personMatches, personStatus, sortPeople, sortWorkspaces, busiestWorkspaces, handGroups, handPeopleCount, peopleCsv, workspacesCsv, daysUntil, isGuest, rolesFor, type Person,
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
    expect(count("locked")).toEqual(["a"])
    expect(count("setup")).toEqual(["b"])
    expect(count("active")).toEqual(["c"])
    expect(count("noaccess")).toEqual(["b"])
    expect(count("all")).toHaveLength(3)
  })
})

describe("needs a hand", () => {
  const ws = (over: Record<string, unknown>) => ({ id: "w", name: "Lab", slug: "lab", created_at: "", _count_members: 1, _count_agents: 0, _count_crews: 0, ...over }) as never
  const people = [
    person({ id: "lock", locked_until: "2026-09-29T13:00:00Z" }),
    person({ id: "setup", setup_link_expires_at: "2026-10-01T00:00:00Z" }),
    person({ id: "nowhere" }),
    person({ id: "gone", suspended_at: "2026-09-01T00:00:00Z" }),
    person({ id: "fine", memberships: [member("w1", "MEMBER")] }),
  ]
  it("does not count anyone twice, and leaves suspended people alone", () => {
    const g = handGroups(people, [ws({ id: "p", kind: "personal" }), ws({ id: "q", kind: "personal", _count_crews: 2 })], NOW)
    expect(g.locked.map((p) => p.id)).toEqual(["lock"])
    expect(g.setup.map((p) => p.id)).toEqual(["setup"])
    expect(g.nowhere.map((p) => p.id)).toEqual(["nowhere"])
    expect(g.emptyAccounts.map((w) => w.id)).toEqual(["p"])
    expect(handPeopleCount(g)).toBe(3)
  })
  it("orders people: needs a hand first, A-Z, last active", () => {
    expect(sortPeople(people, "hand", NOW).map((p) => p.id).slice(0, 3)).toEqual(["lock", "setup", "nowhere"])
    expect(sortPeople([person({ id: "z", full_name: "Zed" }), person({ id: "a", full_name: "Ann" })], "az").map((p) => p.id)).toEqual(["a", "z"])
    const recent = sortPeople([person({ id: "old", last_active_at: "2026-01-01T00:00:00Z" }), person({ id: "new", last_active_at: "2026-09-01T00:00:00Z" }), person({ id: "never" })], "recent")
    expect(recent.map((p) => p.id)).toEqual(["new", "old", "never"])
  })
})

describe("workspace order and the busiest", () => {
  const w = (id: string, runs: number, created: string, over: Record<string, unknown> = {}) =>
    ({ id, name: id, slug: id, created_at: created, runs_7d: runs, _count_members: 3, _count_agents: 0, _count_crews: 0, kind: "team", ...over }) as never
  const list = [w("b", 5, "2026-01-01T00:00:00Z"), w("a", 9, "2026-03-01T00:00:00Z"), w("c", 1, "2026-06-01T00:00:00Z"), w("me", 99, "2026-06-01T00:00:00Z", { kind: "personal" })]
  it("sorts busiest, A-Z and newest", () => {
    expect(sortWorkspaces(list, "busy").map((x) => x.id)).toEqual(["me", "a", "b", "c"])
    expect(sortWorkspaces(list, "az").map((x) => x.id)).toEqual(["a", "b", "c", "me"])
    expect(sortWorkspaces(list, "new").map((x) => x.id).slice(0, 2).sort()).toEqual(["c", "me"])
  })
  it("names the top team workspaces only, never a personal account", () => {
    expect(busiestWorkspaces(list, 2).map((x) => x.id)).toEqual(["a", "b"])
  })
})

describe("CSV export", () => {
  it("writes the rows, quotes commas and defuses formulas", () => {
    const csv = peopleCsv([person({ full_name: "=SUM(A1)", email: "a@x.example", memberships: [member("Lab, East", "OWNER")], instance_admin: true })], NOW)
    const [head, row] = csv.trim().split("\n")
    expect(head).toBe("Name,Email,Workspaces,Last active,Status,Instance admin,Sessions")
    expect(row).toBe("'=SUM(A1),a@x.example,\"Lab, East (owner)\",,Active,yes,0")
    expect(workspacesCsv([{ id: "w", name: "Lab", slug: "lab", created_at: "c", _count_members: 1, _count_agents: 2, _count_crews: 3, runs_7d: 4 } as never], [])).toContain("Lab,lab,,1,3,2,4,,c")
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

describe("team and personal workspaces", () => {
  const ws = (over: Record<string, unknown>) => ({ id: "w", name: "Lab", slug: "lab", created_at: "", _count_members: 1, _count_agents: 0, _count_crews: 0, ...over }) as never

  it("reads the server's kind, and the sign-up pattern from an older server", () => {
    expect(isPersonal(ws({ kind: "personal", name: "Design" }))).toBe(true)
    expect(isPersonal(ws({ kind: "team", name: "Ivana's Workspace" }))).toBe(false)
    expect(isPersonal(ws({ name: "Ivana's Workspace" }))).toBe(true)
    expect(isPersonal(ws({ name: "Ivana's Workspace", _count_members: 3 }))).toBe(false)
  })

  it("finds a workspace by its owner's name or email", () => {
    const owner = { id: "u1", email: "ivana@ex.com", full_name: "Ivana Viewer", memberships: [{ workspace_id: "w", role: "OWNER" }] } as never
    expect(workspaceMatches(ws({ name: "Ivana Viewer's Workspace" }), "viewer", [owner])).toBe(true)
    expect(workspaceMatches(ws({ name: "Design" }), "ivana@", [owner])).toBe(true)
    expect(workspaceMatches(ws({ name: "Design" }), "tom", [owner])).toBe(false)
  })
})

describe("Guests and administrator roles", () => {
  it("names a person a Guest when any membership is restricted, and leaves Admin and Owner out for them", () => {
    const guest = person({ memberships: [member("a", "MEMBER"), { ...member("b", "VIEWER"), access_mode: "restricted" }] })
    expect(isGuest(guest)).toBe(true)
    expect(isGuest(person({ memberships: [{ ...member("a", "MEMBER"), access_mode: "trusted" }] }))).toBe(false)
    // An older server sends no access_mode: nothing is guessed.
    expect(isGuest(person({ memberships: [member("a", "MEMBER")] }))).toBe(false)
    expect(rolesFor(true)).toEqual(["MANAGER", "MEMBER", "VIEWER"])
    expect(rolesFor(false)).toEqual(["OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"])
  })
})
