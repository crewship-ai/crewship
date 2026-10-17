import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"

// F-02 merged the Journal into Activity; /journal only redirects now
// (app/(dashboard)/journal/page.tsx, lib/journal-redirect.ts). A link the app
// builds itself goes straight to Activity (ACT-05) — through the redirect it
// costs a hop, a flash of an empty page and, for an admin, a different view
// than the member gets from the same link. Only the redirect itself may still
// spell the old address. The CLI's `crewship open journal` is Go and stays
// on the redirect until #3461.

const ROOT = path.resolve(__dirname, "../..")
const DIRS = ["app", "components", "lib", "hooks", "stores"]
const ALLOWED = new Set([path.join("app", "(dashboard)", "journal", "page.tsx"), path.join("lib", "journal-redirect.ts")])
// A string or template literal that starts with /journal and ends there or
// continues with a query, a hash or a sub-path. Not /journal-anything.
const LINK = /["'`]\/journal(?=["'`?#/])/

function files(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name)
    if (name === "node_modules" || name === "__tests__" || name === "generated" || name.startsWith(".")) continue
    const st = statSync(p)
    if (st.isDirectory()) files(p, out)
    else if (/\.(tsx?|jsx?)$/.test(name) && !/\.(test|spec)\.[tj]sx?$/.test(name)) out.push(p)
  }
  return out
}

describe("in-app links to the old Journal", () => {
  it("none remain outside the redirect", () => {
    const found: string[] = []
    for (const d of DIRS) {
      for (const f of files(path.join(ROOT, d))) {
        const rel = path.relative(ROOT, f)
        if (ALLOWED.has(rel)) continue
        readFileSync(f, "utf8")
          .split("\n")
          .forEach((line, i) => {
            const code = line.trim()
            // Comments may still talk about the old address.
            if (code.startsWith("//") || code.startsWith("*") || code.startsWith("/*")) return
            if (LINK.test(line)) found.push(`${rel}:${i + 1}`)
          })
      }
    }
    expect(found, "link to /activity (lib/journal-redirect.ts has the mapping)").toEqual([])
  })
})
