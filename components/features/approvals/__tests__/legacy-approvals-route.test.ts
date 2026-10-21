import { describe, it, expect } from "vitest"
import { existsSync } from "node:fs"
import { join } from "node:path"

// The no-JavaScript path of /approvals is a <noscript> meta refresh that has
// to be in the HTML the static export writes for the route. Under
// app/(dashboard) it never is: the dashboard layout renders only a spinner
// until the client session resolves, so the exported approvals.html carried
// no <noscript> at all and a browser without JavaScript stayed on the spinner
// (seen on dev1, which serves the export through the Go backend). The route
// therefore lives in a group with no layout of its own, under the root layout,
// which prerenders its children.
const APP = join(__dirname, "../../../../app")

describe("/approvals route placement", () => {
  it("is not under the client-gated dashboard layout", () => {
    expect(existsSync(join(APP, "(dashboard)/approvals/page.tsx"))).toBe(false)
  })

  it("lives in the layout-less legacy group", () => {
    expect(existsSync(join(APP, "(legacy)/approvals/page.tsx"))).toBe(true)
    expect(existsSync(join(APP, "(legacy)/layout.tsx"))).toBe(false)
  })
})
