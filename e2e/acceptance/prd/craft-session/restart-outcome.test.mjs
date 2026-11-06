// node --test e2e/acceptance/prd/craft-session/restart-outcome.test.mjs
import assert from "node:assert/strict"
import { test } from "node:test"
import { restartNote, restartOutcome } from "./restart-outcome.ts"

const refused = 'failed: { "outcome": "failed", "error": "protected process identity changed during runtime control" }'

test("a refusal after a real restart is a restart, and keeps the complaint", () => {
  assert.deepEqual(restartOutcome(refused, 1800, 3), { kind: "restarted", refusedBy: refused })
})
test("a refusal with the old process still up is a refused restart, not a product failure", () => {
  assert.deepEqual(restartOutcome(refused, 1800, 1812), { kind: "refused", reason: refused })
})
test("a refusal with the server down keeps waiting: health says nothing about the old process", () => {
  assert.equal(restartOutcome(refused, 1800, null).kind, "waiting")
})
test("a refusal without a baseline is not proof of a restart, however young the server looks", () => {
  assert.deepEqual(restartOutcome(refused, null, 3), { kind: "refused", reason: refused })
  assert.deepEqual(restartOutcome(refused, null, 1800), { kind: "refused", reason: refused })
})
test("an accepted restart without a baseline still counts once the server answers", () => {
  assert.deepEqual(restartOutcome("ok", null, 3), { kind: "restarted" })
})
test("an accepted restart waits for the new process", () => {
  assert.deepEqual(restartOutcome("ok", 1800, 1801), { kind: "waiting" })
  assert.deepEqual(restartOutcome("ok", 1800, 2), { kind: "restarted" })
})
test("the row's steps carry the refusal text", () => {
  assert.equal(restartNote({ controlRefused: refused }), `restart: the server did restart, the control refused: ${refused}`)
  assert.equal(restartNote({}), "restart: done")
})
