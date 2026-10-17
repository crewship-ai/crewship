import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join } from "node:path"

// One component called EmptyState (#3344).
//
// There were two: components/layout/empty-state.tsx and one in
// components/ui/detail, with different props and a different look, and a
// file picked one or the other depending on which import auto-complete
// offered first. A third, a text-only placeholder, lived in the crew
// bottom panel. The name now belongs to the layout component alone; any
// other module that exports something called EmptyState — defined there or
// re-exported — fails here.

const ROOTS = ["app", "components", "lib", "hooks", "stores", "ee"]
const HOME = join("components", "layout", "empty-state.tsx")

function sourceFilesUnder(dir: string): string[] {
  let entries: string[]
  try {
    entries = readdirSync(dir)
  } catch {
    return []
  }
  const out: string[] = []
  for (const entry of entries) {
    if (entry === "node_modules" || entry === ".next" || entry === "__tests__") continue
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) out.push(...sourceFilesUnder(full))
    else if (/\.(ts|tsx)$/.test(entry) && !/\.(test|spec)\.tsx?$/.test(entry)) out.push(full)
  }
  return out
}

/** Statements in `source` that export a binding named exactly `EmptyState`. */
function emptyStateExports(source: string): string[] {
  const hits: string[] = []
  const declared = /export\s+(?:default\s+)?(?:async\s+)?(?:function|const|let|var|class)\s+EmptyState\b/g
  for (const m of source.matchAll(declared)) hits.push(m[0])
  // export { EmptyState }, export { Foo as EmptyState }, export { EmptyState } from "…"
  for (const m of source.matchAll(/export\s+(?:type\s+)?\{([^}]*)\}/g)) {
    for (const spec of m[1].split(",")) {
      const exported = spec.trim().split(/\s+as\s+/).pop()?.trim()
      if (exported === "EmptyState") hits.push(m[0].replace(/\s+/g, " "))
    }
  }
  return hits
}

describe("there is one EmptyState", () => {
  it("is exported only from components/layout/empty-state.tsx", () => {
    const offenders: string[] = []
    let home = 0
    for (const root of ROOTS) {
      for (const file of sourceFilesUnder(root)) {
        const hits = emptyStateExports(readFileSync(file, "utf8"))
        if (file === HOME) home = hits.length
        else for (const hit of hits) offenders.push(`${file}: ${hit}`)
      }
    }
    expect(offenders, offenders.join("\n")).toEqual([])
    expect(home).toBe(1)
  })
})

describe("the EmptyState detector itself", () => {
  it("catches definitions and re-exports", () => {
    expect(emptyStateExports(`export function EmptyState({ children }) {}`)).toHaveLength(1)
    expect(emptyStateExports(`export const EmptyState = () => null`)).toHaveLength(1)
    expect(emptyStateExports(`export { EmptyState, FieldLabel, Pill } from "@/components/ui/detail"`)).toHaveLength(1)
    expect(emptyStateExports(`export { Blank as EmptyState }`)).toHaveLength(1)
  })

  it("leaves other names and local helpers alone", () => {
    expect(emptyStateExports(`export function RecipesEmptyState() {}`)).toEqual([])
    expect(emptyStateExports(`export type { EmptyStateProps } from "./x"`)).toEqual([])
    expect(emptyStateExports(`function EmptyState() {}`)).toEqual([])
    expect(emptyStateExports(`export { EmptyState as Blank }`)).toEqual([])
  })
})
