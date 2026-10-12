import { describe, it, expect } from "vitest"

import { ALL_CAPABILITIES } from "@/lib/capabilities"
import { CAPABILITY_GROUPS } from "@/components/admin/member-capabilities"
import {
  DEFAULT_MEMBER_FILTERS,
  activeFilterCount,
  byRoleThenName,
  accessProfile,
  cellState,
  profileText,
  CAPABILITY_COLUMNS,
  extraCapabilities,
  memberMatches,
  membersUrlQuery,
  parseMembersUrl,
  roleChoices,
  type Member,
} from "../members-model"

function member(id: string, role: string, name: string | null, email = `${id}@x.io`): Member {
  return { id: `m-${id}`, role, created_at: new Date().toISOString(), user: { id, email, full_name: name, avatar_url: null } }
}

const olive = member("u-olive", "OWNER", "Olive Owner")
const mel = member("u-mel", "MEMBER", "Mel Member")
const vic = member("u-vic", "VIEWER", null, "vic@example.com")
const caps = { "u-olive": ["chat", "routine.create"], "u-mel": ["chat", "issue.create", "credentials:reveal"], "u-vic": ["chat"] }

describe("memberMatches — the filters combine", () => {
  const f = (over: Partial<typeof DEFAULT_MEMBER_FILTERS>) => ({ ...DEFAULT_MEMBER_FILTERS, ...over })

  it("keeps everyone with no filter", () => {
    expect([olive, mel, vic].filter((m) => memberMatches(m, DEFAULT_MEMBER_FILTERS, caps))).toHaveLength(3)
  })

  it("filters by role", () => {
    expect([olive, mel, vic].filter((m) => memberMatches(m, f({ role: "MEMBER" }), caps))).toEqual([mel])
  })

  it("filters by capability, holders only", () => {
    expect([olive, mel, vic].filter((m) => memberMatches(m, f({ cap: "credentials:reveal" }), caps))).toEqual([mel])
    // chat comes with every role, so everyone holds it.
    expect([olive, mel, vic].filter((m) => memberMatches(m, f({ cap: "chat" }), caps))).toHaveLength(3)
  })

  it("searches name and email, case-insensitively", () => {
    expect([olive, mel, vic].filter((m) => memberMatches(m, f({ q: "OLIVE" }), caps))).toEqual([olive])
    expect([olive, mel, vic].filter((m) => memberMatches(m, f({ q: "example.com" }), caps))).toEqual([vic])
  })

  it("ANDs role, capability and search", () => {
    expect(memberMatches(mel, f({ role: "MEMBER", cap: "issue.create", q: "mel" }), caps)).toBe(true)
    expect(memberMatches(mel, f({ role: "MEMBER", cap: "routine.create" }), caps)).toBe(false)
    expect(memberMatches(mel, f({ role: "OWNER", cap: "issue.create" }), caps)).toBe(false)
  })

  it("ignores a capability filter when capabilities cannot be read (non-admin)", () => {
    // A shared ?cap= link opened by a MEMBER must not empty the roster.
    expect(memberMatches(vic, f({ cap: "credentials:reveal" }), undefined)).toBe(true)
    expect(activeFilterCount(f({ cap: "credentials:reveal" }), false)).toBe(0)
    expect(activeFilterCount(f({ cap: "credentials:reveal", role: "ADMIN", q: "x" }), true)).toBe(3)
  })
})

describe("extraCapabilities — only what the role does not bring", () => {
  it("drops chat, which comes with every role", () => {
    expect(extraCapabilities(["chat"])).toEqual([])
    expect(extraCapabilities(["chat", "issue.create"])).toEqual(["issue.create"])
  })

  it("lists grants in the vocabulary's low → high stakes order", () => {
    expect(extraCapabilities(["credentials:reveal", "chat", "routine.create"])).toEqual(["routine.create", "credentials:reveal"])
  })

  it("treats a person with no stored row as holding nothing extra", () => {
    expect(extraCapabilities(undefined)).toEqual([])
  })
})

describe("accessProfile — the nearest preset plus what differs", () => {
  const POWER = ["chat", "routine.create", "routine.run", "issue.create", "memory.write"]
  const BUILDER = [...POWER, "skill.create", "credential.create", "credential.rotate", "credentials:reveal"]

  it("names an exact preset with no exceptions", () => {
    expect(profileText(accessProfile(["chat"]))).toBe("Chat only")
    expect(profileText(accessProfile(POWER))).toBe("Power user")
    expect(profileText(accessProfile(BUILDER))).toBe("Builder")
  })

  it("spells out what was added and taken away", () => {
    const p = accessProfile([...POWER, "credentials:reveal"])
    expect(p).toEqual({ key: "power", added: ["credentials:reveal"], removed: [] })
    expect(profileText(p)).toBe("Power user +Reveal")
    expect(profileText(accessProfile(POWER.filter((c) => c !== "routine.run")))).toBe("Power user −Run")
  })

  it("prefers the larger preset on a tie", () => {
    // Builder −Skills (1) beats Power user plus three credential grants.
    expect(accessProfile(BUILDER.filter((c) => c !== "skill.create")).key).toBe("admin")
    // Power user +Skills +Create credentials (2) ties Builder −Rotate −Reveal (2).
    expect(accessProfile([...POWER, "skill.create", "credential.create"]).key).toBe("admin")
  })

  it("marks each cell as from the profile, an exception, or off", () => {
    const granted = [...POWER.filter((c) => c !== "routine.run"), "page.create"]
    const p = accessProfile(granted)
    expect(cellState(p, granted, "routine.create")).toBe("profile")
    expect(cellState(p, granted, "routine.run")).toBe("removed")
    expect(cellState(p, granted, "page.create")).toBe("added")
    expect(cellState(p, granted, "credential.create")).toBe("off")
  })

  it("lays every capability but chat out in exactly one column", () => {
    const cols = CAPABILITY_COLUMNS.flatMap((g) => g.caps.map((c) => c.cap))
    expect([...cols].sort()).toEqual(ALL_CAPABILITIES.filter((c) => c !== "chat").sort())
  })
})

describe("memberMatches — profile and needs-a-look filters", () => {
  const f = (over: Partial<typeof DEFAULT_MEMBER_FILTERS>) => ({ ...DEFAULT_MEMBER_FILTERS, ...over })
  it("finds exact profiles, exceptions, revealers and high-stakes below Manager", () => {
    const all = [olive, mel, vic]
    expect(all.filter((m) => memberMatches(m, f({ profile: "chat" }), caps)).map((m) => m.user.id)).toEqual(["u-vic"])
    expect(all.filter((m) => memberMatches(m, f({ profile: "custom" }), caps)).map((m) => m.user.id)).toEqual(["u-mel"])
    expect(all.filter((m) => memberMatches(m, f({ flag: "reveal" }), caps)).map((m) => m.user.id)).toEqual(["u-mel"])
    expect(all.filter((m) => memberMatches(m, f({ flag: "risky-low-role" }), caps)).map((m) => m.user.id)).toEqual(["u-mel"])
  })
})

describe("CAPABILITY_GROUPS", () => {
  it("puts every capability in exactly one group", () => {
    const grouped = CAPABILITY_GROUPS.flatMap((g) => g.caps)
    expect([...grouped].sort()).toEqual([...ALL_CAPABILITIES].sort())
    expect(new Set(grouped).size).toBe(grouped.length)
  })

  it("marks the credentials group high-stakes", () => {
    expect(CAPABILITY_GROUPS.find((g) => g.caps.includes("credentials:reveal"))?.highStakes).toBe(true)
  })
})

describe("roleChoices — the server's ladder, drawn the same way", () => {
  it("lets an OWNER move anyone below them up to Admin", () => {
    expect(roleChoices("OWNER", mel, false)).toEqual(["VIEWER", "MEMBER", "MANAGER", "ADMIN"])
  })

  it("lets an ADMIN grant only below Admin, and never touch an OWNER", () => {
    expect(roleChoices("ADMIN", mel, false)).toEqual(["VIEWER", "MEMBER", "MANAGER"])
    expect(roleChoices("ADMIN", olive, false)).toEqual([])
  })

  it("never offers a change on the caller's own row", () => {
    expect(roleChoices("OWNER", olive, true)).toEqual([])
  })

  it("gives a MEMBER nothing, even over a VIEWER — role change is MANAGER+", () => {
    expect(roleChoices("MEMBER", vic, false)).toEqual([])
    expect(roleChoices(undefined, vic, false)).toEqual([])
  })
})

describe("URL state", () => {
  it("round-trips the person and the filters", () => {
    const state = { member: "u-mel", filters: { role: "ADMIN", cap: "credentials:reveal", profile: "custom", flag: "reveal" as const, q: "ann" } }
    expect(parseMembersUrl(membersUrlQuery(state))).toEqual(state)
  })

  it("keeps defaults out of the URL", () => {
    expect(membersUrlQuery({ member: "", filters: DEFAULT_MEMBER_FILTERS })).toBe("")
  })

  it("drops a role or capability it does not know", () => {
    const s = parseMembersUrl("?role=GOD&cap=root")
    expect(s.filters.role).toBe("")
    expect(s.filters.cap).toBe("")
  })

  it("reads the palette's ?member= link", () => {
    expect(parseMembersUrl("?member=u-fredy").member).toBe("u-fredy")
  })
})

describe("byRoleThenName", () => {
  it("puts higher roles first, then sorts by name or email", () => {
    const a = member("u-a", "MEMBER", "Zed")
    const b = member("u-b", "MEMBER", "Amy")
    expect([vic, a, olive, b].sort(byRoleThenName).map((m) => m.user.id)).toEqual(["u-olive", "u-b", "u-a", "u-vic"])
  })
})
