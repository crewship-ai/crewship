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
test("no baseline proves nothing, whatever the control answered and however young the server looks", () => {
  for (const answer of [refused, "ok"]) {
    for (const uptime of [3, 1800, null]) assert.equal(restartOutcome(answer, null, uptime).kind, "inconclusive")
  }
})
test("an accepted restart waits for the new process", () => {
  assert.deepEqual(restartOutcome("ok", 1800, 1801), { kind: "waiting" })
  assert.deepEqual(restartOutcome("ok", 1800, 2), { kind: "restarted" })
})
test("the row's steps carry the refusal text", () => {
  assert.equal(restartNote({ controlRefused: refused }), `restart: the server did restart, the control refused: ${refused}`)
  assert.equal(restartNote({}), "restart: done")
})
