import { readFileSync } from "node:fs"
import { join } from "node:path"
import { describe, expect, it } from "vitest"

import { LANGUAGES } from "@/lib/languages"

// The picker and the API used to be two unrelated allowlists. The browser
// defaulted en-US to "English (US)", while the API rejected that exact value,
// so every US-locale fresh install stopped on the first Continue button.
describe("onboarding language contract", () => {
  it("the API accepts every name and code the picker can submit", () => {
    const source = readFileSync(join(process.cwd(), "internal", "api", "workspaces.go"), "utf8")
    for (const language of LANGUAGES) {
      expect(source, `missing backend language name ${language.name}`).toContain(
        `${JSON.stringify(language.name)}: true`,
      )
      expect(source, `missing backend language code ${language.code}`).toContain(
        `${JSON.stringify(language.code)}: ${JSON.stringify(language.name)}`,
      )
    }
  })
})
