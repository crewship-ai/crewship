// node --test e2e/acceptance/prd/env.test.mjs
import assert from "node:assert/strict"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { test } from "node:test"

const home = fs.mkdtempSync(path.join(os.tmpdir(), "prd-env-home-"))
process.env.HOME = home
process.env.USERPROFILE = home
const { storageStatePath } = await import("./env.ts")

const run = (outputDir, email, origin = "https://pages.example.test") => ({
  origin,
  outputDir,
  accounts: { owner: { email, password: "x" } },
})

test("concurrent runs sharing HOME and origin get separate session files", () => {
  const a = storageStatePath(run("/tmp/run-a", "alice@example.test"), "owner")
  const b = storageStatePath(run("/tmp/run-b", "bob@example.test"), "owner")
  assert.notEqual(a, b)
  // Same account, different output directory is still a different run.
  assert.notEqual(a, storageStatePath(run("/tmp/run-b", "alice@example.test"), "owner"))
  // Same account in the same output directory but another origin.
  assert.notEqual(a, storageStatePath(run("/tmp/run-a", "alice@example.test", "https://other.example.test"), "owner"))
})

test("a run keeps one stable file per role and never leaks the account name", () => {
  const env = run("/tmp/run-a", "alice@example.test")
  assert.equal(storageStatePath(env, "owner"), storageStatePath(env, "owner"))
  assert.notEqual(storageStatePath(env, "owner"), storageStatePath(env, "member"))
  assert.ok(!storageStatePath(env, "owner").includes("alice"))
  assert.ok(storageStatePath(env, "owner").startsWith(path.join(home, ".crewship", "e2e-auth")))
})
