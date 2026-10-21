import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

import { ALL_CAPABILITIES, CAPABILITY_BUNDLES } from "../capabilities"

/**
 * lib/capabilities.ts mirrors internal/api/capabilities.go by hand. The admin
 * bundle once lacked `credentials:reveal` here while the server granted it,
 * so Settings › Members described the preset wrong. Read the Go source and
 * hold the mirror to it.
 */
const go = readFileSync(join(process.cwd(), "internal/api/capabilities.go"), "utf8")
const constants = Object.fromEntries([...go.matchAll(/^\s*(Capability\w+)\s*=\s*"([^"]+)"/gm)].map((m) => [m[1], m[2]]))

function goBundle(name: string): string[] {
  const body = go.slice(go.indexOf("func BundleCapabilities"))
  const start = body.indexOf(`case Bundle${name}:`)
  const block = body.slice(start, body.indexOf("}", start))
  return [...block.matchAll(/Capability\w+/g)].map((m) => constants[m[0]]).filter(Boolean)
}

describe("capability bundles match the server", () => {
  it.each([["chat", "Chat"], ["power", "Power"], ["admin", "Admin"]] as const)("%s", (key, goName) => {
    expect([...CAPABILITY_BUNDLES[key]].sort()).toEqual(goBundle(goName).sort())
  })

  it("knows every capability the server does", () => {
    expect([...ALL_CAPABILITIES].sort()).toEqual([...new Set(Object.values(constants))].sort())
  })
})
