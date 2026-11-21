import { describe, it, expect } from "vitest"
import { parseUrl, writeUrl, EMPTY_URL } from "../people-url"

describe("the page's URL", () => {
  it("opens the old links where they used to land", () => {
    expect(parseUrl("?view=access").view).toBe("access")
    expect(parseUrl("?view=accounts").view).toBe("accounts")
    expect(parseUrl("?view=workspaces").view).toBe("workspaces")
    expect(parseUrl("?view=people&facet=admins")).toMatchObject({ view: "admins", facet: "all" })
    expect(parseUrl("?facet=attention").view).toBe("hand")
    expect(parseUrl("?facet=locked")).toMatchObject({ view: "people", facet: "locked" })
    expect(parseUrl("?facet=noaccess").facet).toBe("noaccess")
  })
  it("reads the new views and the person, workspace and search", () => {
    expect(parseUrl("?view=hand").view).toBe("hand")
    expect(parseUrl("?view=suspended").view).toBe("suspended")
    expect(parseUrl("?person=u1&from=workspaces&ws=w1&q=ann&in=w2")).toMatchObject({ person: "u1", from: "workspaces", ws: "w1", q: "ann", in: "w2" })
    expect(parseUrl("?view=nonsense&sort=x")).toMatchObject({ view: "people", sort: "hand" })
  })
  it("writes only what differs from the defaults", () => {
    expect(writeUrl(EMPTY_URL, "https://x.test/admin/people")).toBe("https://x.test/admin/people")
    expect(writeUrl({ ...EMPTY_URL, view: "hand", q: "ann" }, "https://x.test/admin/people?view=access")).toBe("https://x.test/admin/people?view=hand&q=ann")
  })
})
