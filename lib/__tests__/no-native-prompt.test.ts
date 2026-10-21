import { readFileSync, readdirSync, statSync } from "node:fs"
import { extname, join, relative, resolve } from "node:path"

import { describe, expect, it } from "vitest"

/**
 * The browser's own modal dialogs do not belong in the UI.
 *
 * A native prompt is unstyled, blocks the whole page, cannot validate what is
 * typed into it and cannot be driven by a test. Input that needs collecting
 * gets a dialog from the kit instead — the issue editor's link and image URL
 * dialogs (#3343) are the reference.
 *
 * Test files are exempt: they stub the globals to prove they are NOT called.
 */

const ROOTS = ["app", "components"]
const EXTENSIONS = [".ts", ".tsx", ".js", ".jsx", ".mjs"]

/**
 * A bare `prompt(` call, or the global reached through `window`,
 * `globalThis` or `self` (called or passed around). A method on some other
 * object — `rl.prompt(` — and identifiers such as `buildPrompt(` are fine.
 */
const NATIVE_PROMPT = /(?<![\w$.])prompt\(|\b(?:window|globalThis|self)\s*\.\s*prompt\b/

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === "__tests__") continue
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      sourceFiles(full, out)
      continue
    }
    if (!EXTENSIONS.includes(extname(entry))) continue
    if (/\.(test|spec)\./.test(entry)) continue
    out.push(full)
  }
  return out
}

function findViolations(pattern: RegExp): string[] {
  const cwd = process.cwd()
  const hits: string[] = []
  for (const root of ROOTS) {
    for (const file of sourceFiles(resolve(cwd, root))) {
      readFileSync(file, "utf8")
        .split("\n")
        .forEach((line, i) => {
          if (pattern.test(line)) hits.push(`${relative(cwd, file)}:${i + 1}: ${line.trim()}`)
        })
    }
  }
  return hits
}

describe("native browser dialogs", () => {
  it("the prompt pattern catches the call shapes it is meant to", () => {
    for (const bad of ['window.prompt("URL:")', 'const u = prompt("x")', "globalThis.prompt", "self . prompt("]) {
      expect(NATIVE_PROMPT.test(bad), bad).toBe(true)
    }
    for (const ok of ["buildPrompt(text)", "rl.prompt()", "the password prompt (§7)", "systemPrompt: string"]) {
      expect(NATIVE_PROMPT.test(ok), ok).toBe(false)
    }
  })

  it("app/ and components/ never call prompt()", () => {
    expect(findViolations(NATIVE_PROMPT)).toEqual([])
  })
})
