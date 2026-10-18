import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync } from "node:fs"
import path from "node:path"
import postcss from "postcss"

// The token tests read globals.css as text; this one makes sure it is still
// a stylesheet. A stray brace once passed every unit test and failed only in
// the production build (#3350).
describe("stylesheets parse", () => {
  it.each(["app/globals.css", "app/styles/accents.css", "app/styles/styles.css"])("%s is valid CSS", (file) => {
    const css = readFileSync(path.resolve(__dirname, "../../..", file), "utf8")
    expect(() => postcss.parse(css)).not.toThrow()
  })
})

// COM-08 (#3481): the onboarding pane's own gradient was replaced by
// `.surface-pane`; nothing wears `.onboarding-pane` any more, so the rule is
// gone rather than left to rot.
describe("unused onboarding pane CSS", () => {
  const root = path.resolve(__dirname, "../../..")
  const wearing = (dir: string): string[] =>
    readdirSync(path.join(root, dir), { withFileTypes: true }).flatMap((e) => {
      const rel = path.join(dir, e.name)
      if (e.isDirectory()) return e.name === "node_modules" || e.name === "__tests__" ? [] : wearing(rel)
      return /\.(tsx?|mdx?)$/.test(e.name) && readFileSync(path.join(root, rel), "utf8").includes("onboarding-pane") ? [rel] : []
    })

  it("no source file wears .onboarding-pane", () => {
    expect(["app", "components", "lib", "hooks"].flatMap(wearing)).toEqual([])
  })

  it("globals.css has no .onboarding-pane rule", () => {
    const selectors: string[] = []
    postcss.parse(readFileSync(path.join(root, "app/globals.css"), "utf8")).walkRules((r) => {
      selectors.push(r.selector)
    })
    expect(selectors.filter((s) => s.includes(".onboarding-pane"))).toEqual([])
  })
})
