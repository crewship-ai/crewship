/** The profile boundary of docs/specs/mooring-profiles.md, executable: the
 * root `lib/mooring` stays the in-frame editor SDK and never re-exports host
 * modules; the experimental `lib/mooring/sdk` entry does not export the host's
 * save factory, private journal opener or admission client, and runtime-imports
 * only an allowlisted set of names from host modules. */
import { readdirSync, readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { describe, expect, it } from "vitest"
import * as root from "../.."
import * as sdk from ".."

const mooring = resolve(__dirname, "../..")
const HOST_EXPORTS = ["createSingleEditorSave", "openNativeDraftJournal", "openRecoveryJournal", "openJournalWithRetry", "createNativeDocumentClient", "parseNativeVersionList", "parseNativeRestoreReceipt", "parseNativeVersionDocument"]
const HOST_MODULE = /\/(single-editor-save|native-draft|native-document-client|native-journal-open|native-versions|native-read-loss|native-editor-port|native-preview-client|wordcraft-[^/]*|gridcraft-[^/]*|sdk\/[^/]*)$|\/hooks\/|api-fetch|server-base|protected-document-drafts|document-queue/

/** Every `from "…"` / `import("…")` of a file, with whether it is type-only. */
function imports(file: string) {
  const out: { spec: string; names: string[]; typeOnly: boolean }[] = []
  const source = readFileSync(file, "utf8")
  for (const m of source.matchAll(/^(import|export)\s+(type\s+)?(\*|\{[^}]*\}|[\w$]+)?(?:\s+as\s+[\w$]+)?\s*(?:from\s+)?"([^"]+)"/gm)) {
    const names = (m[3] ?? "").replace(/[{}]/g, "").split(",").map(s => s.trim()).filter(Boolean)
    const typeOnly = Boolean(m[2]) || names.length > 0 && names.every(n => n.startsWith("type "))
    out.push({ spec: m[4], names: names.filter(n => !n.startsWith("type ")).map(n => n.split(/\s+as\s+/)[0]), typeOnly })
  }
  for (const m of source.matchAll(/import\(\s*"([^"]+)"\s*\)/g)) out.push({ spec: m[1], names: ["*"], typeOnly: false })
  return out
}
const resolveSpec = (from: string, spec: string) => spec.startsWith(".") ? resolve(dirname(from), spec).replace(/\.tsx?$/, "") : spec

describe("Mooring SDK boundary", () => {
  it("keeps the root entry the in-frame editor SDK", () => {
    expect(Object.keys(root).sort()).toEqual(["MOORING_FRAME_ATTRIBUTES", "MOORING_PROTOCOL", "capabilityPort", "createBrowserEditorEnvironment", "createCapabilityChannel", "createEditorSDK", "durableReceipt", "helloAfterLoad", "parseRevisionPush", "transferEditorPort", "verifyEditorHello"])
    for (const name of [...HOST_EXPORTS, "createLeaseActions", "recoveryOffers", "createVersionHistory", "saveLabel"]) expect(root, name).not.toHaveProperty(name)
  })

  it("never lets the root entry reach a host module or the SDK, also transitively", () => {
    const seen = new Set<string>(), reached: string[] = []
    const walk = (file: string) => {
      if (seen.has(file)) return
      seen.add(file)
      for (const { spec, typeOnly } of imports(file + ".ts")) {
        if (typeOnly || !spec.startsWith(".")) continue
        const target = resolveSpec(file + ".ts", spec)
        if (HOST_MODULE.test(target)) reached.push(target.slice(mooring.length))
        else walk(target)
      }
    }
    walk(resolve(mooring, "index"))
    expect(reached).toEqual([])
    expect(seen.size).toBeGreaterThan(3)
  })

  it("does not export the host's save factory, journal opener or admission client from the SDK", () => {
    for (const name of HOST_EXPORTS) expect(sdk, name).not.toHaveProperty(name)
  })

  it("runtime-imports only allowlisted names from host modules", () => {
    const allowed: Record<string, string[]> = {
      "/index": ["*"],
      "/native-draft": ["validNativeVersion"],
      "/single-editor-save": ["NativeSaveError"],
    }
    const found: string[] = []
    const dir = resolve(mooring, "sdk")
    for (const name of readdirSync(dir).filter(f => f.endsWith(".ts"))) {
      for (const { spec, names, typeOnly } of imports(resolve(dir, name))) {
        if (typeOnly) continue
        const target = resolveSpec(resolve(dir, name), spec)
        if (target.startsWith(dir + "/")) continue
        const key = target.startsWith(mooring) ? target.slice(mooring.length) : target
        const ok = allowed[key]
        for (const imported of names.length ? names : ["*"]) if (!ok || !(ok.includes("*") || ok.includes(imported))) found.push(`${name}: ${imported} from ${key}`)
      }
    }
    expect(found).toEqual([])
  })
})
