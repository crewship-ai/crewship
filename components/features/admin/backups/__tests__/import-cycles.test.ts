// ADM-03 (#3493): Admin › Data retention and the Backups console must not
// import each other. `data-retention.tsx` took `useBackupsScope` and
// `SectionCtx` from `backups-console.tsx`, which itself renders
// `DataRetention` — a module cycle that makes either file's evaluation order
// depend on which one a page imports first. The scope hook and the section
// context live in their own module that neither page owns.
//
// A small import graph of this folder, type imports included (madge counts
// them too): every relative import resolved to a file, then a depth-first
// search for a cycle.
import fs from "node:fs"
import path from "node:path"

const DIR = path.resolve(__dirname, "..")

function sources(): string[] {
  return fs.readdirSync(DIR).filter((f) => /\.(ts|tsx)$/.test(f))
}

function resolve(spec: string): string | null {
  if (!spec.startsWith("./")) return null
  const base = spec.slice(2)
  for (const ext of [".ts", ".tsx"]) {
    if (fs.existsSync(path.join(DIR, base + ext))) return base + ext
  }
  return null
}

function graph(): Map<string, string[]> {
  const g = new Map<string, string[]>()
  for (const file of sources()) {
    const text = fs.readFileSync(path.join(DIR, file), "utf8")
    const deps = new Set<string>()
    for (const m of text.matchAll(/(?:import|export)\s[^"']*?from\s+["']([^"']+)["']/g)) {
      const dep = resolve(m[1])
      if (dep) deps.add(dep)
    }
    g.set(file, [...deps])
  }
  return g
}

function cycles(g: Map<string, string[]>): string[][] {
  const found: string[][] = []
  const state = new Map<string, "open" | "done">()
  const stack: string[] = []
  const visit = (n: string) => {
    state.set(n, "open")
    stack.push(n)
    for (const d of g.get(n) ?? []) {
      if (state.get(d) === "open") found.push([...stack.slice(stack.indexOf(d)), d])
      else if (!state.has(d)) visit(d)
    }
    stack.pop()
    state.set(n, "done")
  }
  for (const n of g.keys()) if (!state.has(n)) visit(n)
  return found
}

describe("admin/backups import graph", () => {
  it("reads the folder's relative imports", () => {
    // Guards the parser: if it found nothing, "no cycles" would mean nothing.
    expect(graph().get("backups-console.tsx")).toContain("data-retention.tsx")
  })

  it("Data retention does not import the Backups console", () => {
    expect(graph().get("data-retention.tsx")).not.toContain("backups-console.tsx")
  })

  it("has no import cycle", () => {
    expect(cycles(graph()).map((c) => c.join(" → "))).toEqual([])
  })
})
