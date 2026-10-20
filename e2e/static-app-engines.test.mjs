import { test } from "node:test"
import assert from "node:assert/strict"
import { spawnSync } from "node:child_process"
import { requireBrowserRuns, selectEngines } from "./static-app-engines.mjs"

const available = ["chromium", "firefox", "webkit"]

test("unset selects every known engine", () => {
  assert.deepEqual(selectEngines(undefined, available), available)
})

test("a valid list narrows the run, tolerating spaces", () => {
  assert.deepEqual(selectEngines("chromium", available), ["chromium"])
  assert.deepEqual(selectEngines(" webkit , chromium ", available), ["chromium", "webkit"])
})

test("empty, blank and separator-only values are rejected", () => {
  for (const value of ["", "   ", ",", " , "]) {
    assert.throws(() => selectEngines(value, available), /STATIC_APP_ENGINES.*empty/i, JSON.stringify(value))
  }
})

test("unknown or misspelled engines are rejected and named", () => {
  assert.throws(() => selectEngines("chromuim", available), /unknown.*chromuim/i)
  assert.throws(() => selectEngines("chromium,edge", available), /unknown.*edge/i)
})

test("zero executed browsers is a failure", () => {
  assert.throws(() => requireBrowserRuns(0), /0 browsers/i)
  assert.doesNotThrow(() => requireBrowserRuns(1))
})

function harness(env) {
  const clean = { ...process.env }
  delete clean.STATIC_APP_ENGINES
  return spawnSync(process.execPath, ["e2e/static-app-preview.browser.mjs"], { env: { ...clean, ...env }, encoding: "utf8", timeout: 120000 })
}

test("harness exits non-zero with a clear message for empty and unknown STATIC_APP_ENGINES", () => {
  for (const value of ["", "chromuim"]) {
    const result = harness({ STATIC_APP_ENGINES: value })
    assert.notEqual(result.status, 0, `STATIC_APP_ENGINES=${JSON.stringify(value)} must fail`)
    assert.match(result.stderr, /STATIC_APP_ENGINES/)
  }
})
