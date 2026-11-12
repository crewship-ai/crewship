/** The profile boundary of docs/specs/mooring-profiles.md, executable: the
 * root `lib/mooring` stays the in-frame editor SDK and never re-exports host
 * modules; the experimental `lib/mooring/sdk` entry does not export the host's
 * save factory, private journal opener or admission client, and runtime-imports
 * only an allowlisted set of names from host modules. */
import { existsSync, readdirSync, readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import ts from "typescript"
import { describe, expect, it } from "vitest"
import * as root from "../.."
import * as sdk from ".."
import * as nativeDocumentClient from "../../native-document-client"
import * as nativeDraft from "../../native-draft"
import * as nativeJournalOpen from "../../native-journal-open"
import * as nativeVersions from "../../native-versions"
import * as singleEditorSave from "../../single-editor-save"

const mooring = resolve(__dirname, "../..")
const repo = resolve(mooring, "../..")
const HOST_EXPORTS = ["createSingleEditorSave", "openNativeDraftJournal", "openRecoveryJournal", "openJournalWithRetry", "createNativeDocumentClient", "parseNativeVersionList", "parseNativeRestoreReceipt", "parseNativeVersionDocument"]
const HOST_MODULE = /\/(single-editor-save|native-draft|native-document-client|native-journal-open|native-versions|native-read-loss|native-editor-port|native-preview-client|wordcraft-[^/]*|gridcraft-[^/]*|sdk\/[^/]*|sdk)$|\/hooks\/|api-fetch|server-base|protected-document-drafts|document-queue/
/** Runtime values the SDK may hand out that are also host-module exports. */
const ALLOWED_HOST_VALUES = new Set<unknown>([singleEditorSave.NativeSaveError, nativeDraft.validNativeVersion])

/** Every module reference of a file, parsed by TypeScript: static imports and
 * re-exports (with their runtime names; type-only ones flagged), `import x =
 * require()`, and dynamic `import()` / `require()` calls. */
function imports(file: string) {
  const out: { spec: string; names: string[]; typeOnly: boolean }[] = []
  const source = ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true, file.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
  const visit = (node: ts.Node) => {
    if (ts.isImportDeclaration(node) && ts.isStringLiteral(node.moduleSpecifier)) {
      const clause = node.importClause, names: string[] = []
      let typeOnly = Boolean(clause?.isTypeOnly)
      if (clause && !typeOnly) {
        if (clause.name) names.push("default")
        const bindings = clause.namedBindings
        if (bindings && ts.isNamespaceImport(bindings)) names.push("*")
        if (bindings && ts.isNamedImports(bindings)) for (const e of bindings.elements) if (!e.isTypeOnly) names.push((e.propertyName ?? e.name).text)
        typeOnly = !clause.name && !!bindings && ts.isNamedImports(bindings) && bindings.elements.length > 0 && names.length === 0
      }
      out.push({ spec: node.moduleSpecifier.text, names: clause ? names : ["*"], typeOnly })
    } else if (ts.isExportDeclaration(node) && node.moduleSpecifier && ts.isStringLiteral(node.moduleSpecifier)) {
      const names: string[] = []
      if (!node.exportClause) names.push("*")
      else if (ts.isNamespaceExport(node.exportClause)) names.push("*")
      else for (const e of node.exportClause.elements) if (!e.isTypeOnly) names.push((e.propertyName ?? e.name).text)
      out.push({ spec: node.moduleSpecifier.text, names, typeOnly: node.isTypeOnly || names.length === 0 })
    } else if (ts.isImportEqualsDeclaration(node) && ts.isExternalModuleReference(node.moduleReference) && ts.isStringLiteral(node.moduleReference.expression)) {
      out.push({ spec: node.moduleReference.expression.text, names: ["*"], typeOnly: node.isTypeOnly })
    } else if (ts.isCallExpression(node) && (node.expression.kind === ts.SyntaxKind.ImportKeyword || ts.isIdentifier(node.expression) && node.expression.text === "require")) {
      const [arg] = node.arguments
      out.push({ spec: arg && ts.isStringLiteralLike(arg) ? arg.text : "<computed>", names: ["*"], typeOnly: false })
    }
    ts.forEachChild(node, visit)
  }
  visit(source)
  return out
}
/** Resolves a relative or `@/` specifier to a module path without extension
 * (a directory resolves to its `/index`). Bare package names stay as they are. */
function resolveSpec(from: string, spec: string) {
  const base = spec.startsWith("@/") ? resolve(repo, spec.slice(2)) : spec.startsWith(".") ? resolve(dirname(from), spec) : undefined
  if (!base) return spec
  if (!existsSync(base + ".ts") && !existsSync(base + ".tsx") && (existsSync(base + "/index.ts") || existsSync(base + "/index.tsx"))) return base + "/index"
  return base
}
const fileOf = (module: string) => [".ts", ".tsx"].map(s => module + s).find(existsSync)

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
      const path = fileOf(file)
      if (!path) return
      for (const { spec, typeOnly } of imports(path)) {
        if (typeOnly) continue
        const target = resolveSpec(path, spec)
        if (!target.startsWith("/")) { if (spec === "<computed>") reached.push(spec); continue }
        if (HOST_MODULE.test(target)) reached.push(target.slice(mooring.length))
        else walk(target)
      }
    }
    walk(resolve(mooring, "index"))
    expect(reached).toEqual([])
    expect(seen.size).toBeGreaterThan(3)
  })

  it("does not export the host's save factory, journal opener or admission client from the SDK, under any name", () => {
    for (const name of HOST_EXPORTS) expect(sdk, name).not.toHaveProperty(name)
    const host = new Map<unknown, string>()
    for (const [module, exports] of Object.entries({ singleEditorSave, nativeDraft, nativeDocumentClient, nativeJournalOpen, nativeVersions })) for (const [name, value] of Object.entries(exports)) if (typeof value === "function" || typeof value === "object" && value) host.set(value, `${module}.${name}`)
    const leaked = Object.entries(sdk).filter(([, value]) => host.has(value) && !ALLOWED_HOST_VALUES.has(value)).map(([name, value]) => `${name} = ${host.get(value)}`)
    expect(leaked).toEqual([])
  })

  it("runtime-imports only allowlisted names from host modules", () => {
    const allowed: Record<string, string[]> = {
      "/index": ["*"],
      "/native-draft": ["validNativeVersion"],
      "/single-editor-save": ["NativeSaveError"],
    }
    const found: string[] = []
    const dir = resolve(mooring, "sdk")
    for (const name of readdirSync(dir).filter(f => /\.tsx?$/.test(f))) {
      for (const { spec, names, typeOnly } of imports(resolve(dir, name))) {
        if (typeOnly) continue
        const target = resolveSpec(resolve(dir, name), spec)
        if (target.startsWith(dir + "/") && !target.includes("/__tests__/")) continue
        const key = target.startsWith(mooring) ? target.slice(mooring.length) : target
        const ok = allowed[key]
        for (const imported of names.length ? names : ["*"]) if (!ok || !(ok.includes("*") || ok.includes(imported))) found.push(`${name}: ${imported} from ${key}`)
      }
    }
    expect(found).toEqual([])
  })
})
