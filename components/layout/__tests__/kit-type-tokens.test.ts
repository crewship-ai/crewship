import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"

// The guard's raw-text-size rule (lib/__tests__/design-token-guard.test.ts);
// importing that file would run its suite here too.
const RAW_TEXT_SIZE = /\btext-\[\d+(?:\.\d+)?(?:px|rem)\]/g

// COM-10 (#3481): the shared kit sets the type scale for every page, so its
// own files carry no raw `text-[Npx]`. The guard baseline only stops them
// growing; these files must be at zero.
const ROOT = path.resolve(__dirname, "../../..")
const KIT_FILES = [
  "components/ui/tab-bar.tsx",
  "components/layout/sidebar-kit.tsx",
  "components/layout/sidebar-facets.tsx",
  "components/ui/segmented.tsx",
]

describe("kit type tokens", () => {
  for (const file of KIT_FILES) {
    it(`${file} uses no raw text sizes`, () => {
      const src = readFileSync(path.join(ROOT, file), "utf8")
      expect(src.match(RAW_TEXT_SIZE) ?? []).toEqual([])
    })
  }
})
