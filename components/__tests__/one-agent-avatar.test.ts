import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync, statSync } from "node:fs"
import { join } from "node:path"

// Every agent face goes through <AgentAvatar> (#3344).
//
// Inline avatar URLs drift. Each site that called getAgentAvatarUrl on its
// own picked its own seed precedence, its own style fallback and its own
// way of drawing it (an <img>, a next/image, a CSS background-image), and
// none of them got the stored render or the fall-back-to-generation on a
// failed load that AgentAvatar does. The only way to keep that from coming
// back one site at a time is to check the source, every file.
//
// Allowed: the builder itself (lib/agent-avatar.ts), the one component that
// renders it (components/ui/agent-avatar.tsx), and tests.

const ROOTS = ["app", "components", "lib", "hooks", "stores", "ee"]
const ALLOWED = new Set([join("lib", "agent-avatar.ts"), join("components", "ui", "agent-avatar.tsx")])

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

/** Strip // and /* *\/ comments so prose that names the builder is not a hit. */
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`])\/\/.*$/gm, "$1")
}

/** Lines that build an agent avatar URL rather than rendering <AgentAvatar>. */
function avatarUrlBuilds(source: string): string[] {
  const hits: string[] = []
  const patterns: Array<(line: string) => boolean> = [
    (line) => /\bgetAgentAvatarUrl\s*\(/.test(line), // the shared builder, called outside the component
    (line) => /\bcreateAvatar\s*\(/.test(line), // DiceBear core, called directly
    // The hosted DiceBear HTTP API. A plain substring search, not a regex:
    // this scans source text for the host anywhere on a line, it never
    // decides whether a URL is trusted.
    (line) => line.includes("api.dicebear.com"),
  ]
  for (const line of stripComments(source).split("\n")) {
    if (patterns.some((matches) => matches(line))) hits.push(line.trim())
  }
  return hits
}

describe("every agent avatar renders through AgentAvatar", () => {
  it("builds no avatar URL outside AgentAvatar", () => {
    const offenders: string[] = []
    for (const root of ROOTS) {
      for (const file of sourceFilesUnder(root)) {
        if (ALLOWED.has(file)) continue
        for (const hit of avatarUrlBuilds(readFileSync(file, "utf8"))) offenders.push(`${file}: ${hit}`)
      }
    }
    expect(offenders, offenders.join("\n")).toEqual([])
  })
})

describe("the avatar detector itself", () => {
  it("catches the shapes that shipped", () => {
    expect(avatarUrlBuilds(`<img src={getAgentAvatarUrl(seed, style)} />`)).toHaveLength(1)
    expect(avatarUrlBuilds("style={{ backgroundImage: `url(${getAgentAvatarUrl(a.id)})` }}")).toHaveLength(1)
    expect(avatarUrlBuilds(`const url = getAgentAvatarUrl(\n  seed,\n)`)).toHaveLength(1)
    expect(avatarUrlBuilds(`createAvatar(style, { seed }).toDataUri()`)).toHaveLength(1)
    expect(avatarUrlBuilds(`src="https://api.dicebear.com/9.x/bottts/svg?seed=x"`)).toHaveLength(1)
  })

  it("leaves the component and prose alone", () => {
    expect(avatarUrlBuilds(`<AgentAvatar seed={seed} style={style} className="size-6" />`)).toEqual([])
    expect(avatarUrlBuilds(`// getAgentAvatarUrl(seed) used to be called here`)).toEqual([])
    expect(avatarUrlBuilds(`/* getAgentAvatarUrl(seed) */`)).toEqual([])
  })
})
