import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"
import postcss from "postcss"

// The token tests read globals.css as text; this one makes sure it is still
// a stylesheet. A stray brace once passed every unit test and failed only in
// the production build (#3350).
describe("stylesheets parse", () => {
  it.each(["app/globals.css", "app/styles/accents.css"])("%s is valid CSS", (file) => {
    const css = readFileSync(path.resolve(__dirname, "../../..", file), "utf8")
    expect(() => postcss.parse(css)).not.toThrow()
  })
})
