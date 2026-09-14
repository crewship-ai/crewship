import { readFileSync, readdirSync } from "node:fs"
import { expect, it } from "vitest"

it("T3 every immediate routine start keeps asynchronous delivery and deduplication", () => {
  const directory = "components/features/routines"
  const sources = readdirSync(directory)
    .filter((name) => name.endsWith(".tsx"))
    .map((name) => ({ name, source: readFileSync(`${directory}/${name}`, "utf8") }))
    .filter(
      ({ name, source }) =>
        name === "routines-detail-panel.tsx" ||
        (/\/run[`"']/.test(source) && !/fire_at:/.test(source)),
    )
  expect(sources.map(({ name }) => name).sort()).toEqual([
    "routine-comparison.tsx",
    "routine-run-detail.tsx",
    "routines-detail-panel.tsx",
  ])
  for (const { name, source } of sources) {
    expect(source, name).toMatch(/headers:\s*\{[^}]*["']?Prefer["']?:\s*["']respond-async["']/s)
    expect(source, name).toContain('"Idempotency-Key"')
  }
})
