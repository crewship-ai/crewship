import { readdirSync, readFileSync, statSync } from "node:fs"
import { join, relative } from "node:path"
import { describe, expect, it } from "vitest"

// Harbor 2 forbids the browser's own dialogs: they cannot be styled, tested
// or read as part of the page. Use ConfirmDialog (component state) or
// askConfirm from components/ui/confirm-host (hooks and handlers).
//
// prompt() is covered by the tiptap URL dialog guard, not here.

const ROOT = join(__dirname, "..", "..")
const DIRS = ["app", "components", "hooks", "lib"]
const SKIP_DIR = new Set(["node_modules", "__tests__", "generated", ".next"])

/** Files allowed to call a native dialog, each with the reason. */
const ALLOWED: Record<string, string> = {
  // Documented fallback when no <ConfirmHost /> is mounted (a standalone
  // component root or a unit test). Inside the app shell it uses askConfirm.
  "hooks/use-unsaved-navigation-guard.ts": "fallback without ConfirmHost",
}

function walk(dir: string, out: string[]) {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) {
      if (!SKIP_DIR.has(name)) walk(full, out)
    } else if (/\.(ts|tsx)$/.test(name) && !/\.(test|spec)\./.test(name) && !/fixtures?\./.test(name)) {
      out.push(full)
    }
  }
}

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:\\'"`])\/\/.*$/gm, "$1")
}

export function nativeDialogCalls(src: string): string[] {
  const code = stripComments(src)
  const hits: string[] = []
  for (const m of code.matchAll(/(?:window\.|globalThis\.)(?:confirm|alert)\s*\(/g)) hits.push(m[0])
  // A bare confirm( is a native call unless the file binds its own `confirm`.
  const localConfirm = /(?:const|let|var|function)\s+confirm\b/.test(code)
  for (const m of code.matchAll(/(?<![\w.$])(confirm|alert)\s*\(/g)) {
    if (m[1] === "confirm" && localConfirm) continue
    hits.push(m[0])
  }
  return hits
}

describe("no native browser dialogs", () => {
  it("recognises native calls and local bindings", () => {
    expect(nativeDialogCalls("if (!window.confirm('x')) return")).toHaveLength(1)
    expect(nativeDialogCalls("if (!confirm('x')) return")).toHaveLength(1)
    expect(nativeDialogCalls("alert('x')")).toHaveLength(1)
    expect(nativeDialogCalls("const confirm = f(); confirm(1)")).toHaveLength(0)
    expect(nativeDialogCalls("// window.confirm('x')\n/* alert('y') */")).toHaveLength(0)
  })

  it("production code calls confirm()/alert() only where allowed", () => {
    const files: string[] = []
    for (const d of DIRS) walk(join(ROOT, d), files)
    const offenders: string[] = []
    for (const file of files) {
      const rel = relative(ROOT, file)
      if (ALLOWED[rel]) continue
      const hits = nativeDialogCalls(readFileSync(file, "utf8"))
      if (hits.length) offenders.push(`${rel} (${hits.length})`)
    }
    expect(offenders).toEqual([])
  })
})
