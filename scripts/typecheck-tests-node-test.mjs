import { test } from "node:test"
import assert from "node:assert/strict"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { checkTestTypes, formatDiagnostic } from "./typecheck-tests.mjs"

// A throwaway project, so the gate is exercised against files whose
// diagnostics are known, not against the repo's own (clean) tests.
function project(files) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "typecheck-tests-"))
  fs.writeFileSync(path.join(dir, "tsconfig.json"), JSON.stringify({
    compilerOptions: { strict: true, noEmit: true, types: [], skipLibCheck: true, target: "ES2020" },
    include: ["**/*.test.ts"],
  }))
  for (const [name, source] of Object.entries(files)) fs.writeFileSync(path.join(dir, name), source)
  return path.join(dir, "tsconfig.json")
}

test("a clean project reports nothing", () => {
  assert.deepEqual(checkTestTypes(project({ "ok.test.ts": "export const n: number = 1\n" })), [])
})

test("every diagnostic is reported with its file, line and code", () => {
  const diagnostics = checkTestTypes(project({
    "bad.test.ts": "export const s: string = 1\n\nexport const missing: { id: string } = {}\n",
  }))
  assert.deepEqual(diagnostics.map(d => [d.file, d.line, d.code]), [["bad.test.ts", 1, 2322], ["bad.test.ts", 3, 2741]])
  assert.match(formatDiagnostic(diagnostics[0]), /^bad\.test\.ts:1: TS2322: /)
})

test("a broken tsconfig is an error, not a clean run", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "typecheck-tests-"))
  fs.writeFileSync(path.join(dir, "tsconfig.json"), "{ not json")
  assert.throws(() => checkTestTypes(path.join(dir, "tsconfig.json")))
})
