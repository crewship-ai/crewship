import { describe, it, expect } from "vitest"
import { readdirSync, statSync } from "node:fs"
import path from "node:path"

import { APP_ROUTE_PATTERNS, isKnownAppPath } from "@/lib/app-routes"

const APP = path.resolve(__dirname, "../../app")

/** Every page.tsx under app/, as its URL pattern (route groups dropped). */
function pagesOnDisk(dir = APP, prefix: string[] = []): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === "__tests__" || name === "styles") continue
      const group = name.startsWith("(") && name.endsWith(")")
      out.push(...pagesOnDisk(p, group ? prefix : [...prefix, name]))
    } else if (name === "page.tsx") {
      out.push("/" + prefix.join("/"))
    }
  }
  return out
}

describe("app route list", () => {
  it("matches the pages on disk exactly", () => {
    expect([...APP_ROUTE_PATTERNS].sort()).toEqual(pagesOnDisk().sort())
  })

  it.each([
    ["/", true],
    ["/issues", true],
    ["/issues/", true],
    ["/issues/CRE-12", true],
    ["/chat/scout", true],
    ["/missions/m-1/timeline", true],
    ["/settings/members", true],
    ["/does-not-exist", false],
    ["/issues/CRE-12/extra", false],
    ["/chat/a/b", false],
    ["/settings/nope", false],
    ["/missions/m-1", false],
  ])("%s → %s", (p, known) => {
    expect(isKnownAppPath(p)).toBe(known)
  })
})
