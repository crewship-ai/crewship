import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { beforeEach, describe, expect, it } from "vitest"
import { throwawayRefusal } from "../../e2e/throwaway-guard"

let root: string
let env: Record<string, string | undefined>
const BASE = "http://127.0.0.1:8123"

function instance(over: Record<string, string> = {}) {
  const dir = join(root, "chat-e2e")
  mkdirSync(join(dir, "data"), { recursive: true })
  writeFileSync(join(dir, "manifest.json"), JSON.stringify({ name: "chat-e2e", state: "running", port: "8123", data_dir: join(dir, "data"), ...over }))
  env = { CREWSHIP_THROWAWAY_STATE: root, CREWSHIP_THROWAWAY_MANIFEST: join(dir, "manifest.json"), E2E_CREWSHIP_DATA_DIR: join(dir, "data"), CREWSHIP_SERVER: BASE }
  return dir
}

beforeEach(() => { root = mkdtempSync(join(tmpdir(), "throwaway-")) })

describe("throwawayRefusal (fail closed)", () => {
  it("accepts a running throwaway instance bound to its own data dir and port", () => {
    instance()
    expect(throwawayRefusal(env, BASE, [])).toBe("")
  })
  it("refuses without a manifest or data dir", () => {
    instance()
    expect(throwawayRefusal({ ...env, CREWSHIP_THROWAWAY_MANIFEST: "" }, BASE, [])).toMatch(/throwaway-server/)
    expect(throwawayRefusal({ ...env, E2E_CREWSHIP_DATA_DIR: "" }, BASE, [])).toMatch(/DATA_DIR is not set/)
  })
  it("refuses a data dir that is not the manifest's", () => {
    instance()
    const other = join(root, "other", "data")
    mkdirSync(other, { recursive: true })
    expect(throwawayRefusal({ ...env, E2E_CREWSHIP_DATA_DIR: other }, BASE, [])).toMatch(/not this throwaway/)
  })
  it("refuses a data dir under a shared slot path or outside the state root", () => {
    const dir = instance()
    expect(throwawayRefusal(env, BASE, [root])).toMatch(/shared environment/)
    expect(throwawayRefusal({ ...env, CREWSHIP_THROWAWAY_STATE: join(dir, "elsewhere") }, BASE, [])).toMatch(/cannot be resolved|not under/)
  })
  it("refuses a manifest that is not running, a foreign port or a different base URL", () => {
    instance({ state: "teardown_failed" })
    expect(throwawayRefusal(env, BASE, [])).toMatch(/not running/)
    instance({ port: "8081" })
    expect(throwawayRefusal({ ...env, CREWSHIP_SERVER: "http://127.0.0.1:8081" }, "http://127.0.0.1:8081", [])).toMatch(/outside the throwaway range/)
    instance()
    expect(throwawayRefusal(env, "http://127.0.0.1:8081", [])).toMatch(/base URL/)
    expect(throwawayRefusal(env, "https://chat.example.test", [])).toMatch(/base URL/)
    expect(throwawayRefusal({ ...env, CREWSHIP_SERVER: "http://127.0.0.1:8124" }, BASE, [])).toMatch(/CREWSHIP_SERVER/)
  })
})
