// node --test e2e/acceptance/prd/config.test.mjs
//
// Loads the real playwright.prd-acceptance.config.ts the way Playwright does
// and checks the owner session file it hands to every scenario. Review of
// #4685 found the config still calling storageStatePath(prd.origin, "owner")
// after the helper started taking the whole env: loading the config threw
// before global setup, so the suite could not run at all (#4846).
import assert from "node:assert/strict"
import * as fs from "node:fs"
import { registerHooks } from "node:module"
import * as os from "node:os"
import * as path from "node:path"
import { test } from "node:test"

// Playwright resolves the config's extensionless TypeScript imports itself;
// plain Node does not, so map `./env` to `./env.ts` for imports from .ts files.
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (context.parentURL?.endsWith(".ts") && /^\.\.?\//.test(specifier) && !path.extname(specifier)) {
      try {
        return nextResolve(`${specifier}.ts`, context)
      } catch {
        // Not a TypeScript sibling; resolve as written.
      }
    }
    return nextResolve(specifier, context)
  },
})

const configURL = new URL("../../../playwright.prd-acceptance.config.ts", import.meta.url)

const runEnv = {
  HOME: fs.mkdtempSync(path.join(os.tmpdir(), "prd-config-home-")),
  PRD_MODE: "quick",
  PRD_ORIGIN: "https://pages.example.test",
  PRD_OUTPUT_DIR: fs.mkdtempSync(path.join(os.tmpdir(), "prd-config-out-")),
  PRD_OWNER_EMAIL: "owner@example.test",
  PRD_OWNER_PASSWORD: "x",
}

// The config reads process.env when it is imported; scope the run's variables
// to the import so other test files sharing this process keep their own.
async function withRunEnv(fn) {
  const saved = Object.fromEntries(Object.keys(runEnv).map((k) => [k, process.env[k]]))
  Object.assign(process.env, runEnv)
  try {
    return await fn()
  } finally {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k]
      else process.env[k] = v
    }
  }
}

test("the PRD acceptance config loads and uses this run's owner session file", async () => {
  const { config, expected, other } = await withRunEnv(async () => {
    const { default: config } = await import(configURL.href)
    const { readPrdEnv, storageStatePath } = await import("./env.ts")
    const env = readPrdEnv()
    return { config, expected: storageStatePath(env, "owner"), other: storageStatePath({ ...env, outputDir: `${env.outputDir}-other` }, "owner") }
  })
  assert.equal(config.use.baseURL, runEnv.PRD_ORIGIN)
  assert.equal(config.use.storageState, expected)
  // The file is bound to this run, not shared with another output directory.
  assert.notEqual(config.use.storageState, other)
  assert.ok(config.use.storageState.startsWith(path.join(runEnv.HOME, ".crewship", "e2e-auth")))
})
