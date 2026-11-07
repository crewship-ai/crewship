import { readFileSync } from "node:fs"
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"
import { config, z } from "zod"
import "../zod-config"

describe("zod configuration", () => {
  it("never probes eval, which the CSP refuses", () => {
    expect(config().jitless).toBe(true)
  })

  it("still parses and rejects without compiled parsers", () => {
    const schema = z.object({ id: z.string(), tags: z.array(z.string()).default([]) })
    expect(schema.parse({ id: "a" })).toEqual({ id: "a", tags: [] })
    expect(schema.safeParse({ id: 1 }).success).toBe(false)
  })

  it("loads from the client instrumentation entry, before application modules", () => {
    const source = readFileSync(resolve(process.cwd(), "instrumentation-client.ts"), "utf8")
    expect(source.match(/^import .*$/m)?.[0]).toBe('import "@/lib/zod-config"')
  })
})
