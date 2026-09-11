import { test } from "node:test"
import assert from "node:assert/strict"
import { compareDiagnostics } from "./typecheck-tests.mjs"
const error = { file: "a.test.ts", code: 2322, message: "missing prop", count: 2 }
test("known errors pass and lower counts are allowed", () => {
 assert.deepEqual(compareDiagnostics([error], [error]), [])
 assert.deepEqual(compareDiagnostics([{...error,count:1}], [error]), [])
})
test("increased count, another file, another code, or another message fails", () => {
 for (const changed of [{count:3},{file:"b.test.ts"},{code:2339},{message:"wrong prop"}]) {
  assert.equal(compareDiagnostics([{...error,...changed}], [error]).length,1)
 }
})
test("removing an old error does not buy an allowance for a new one", () => {
 assert.equal(compareDiagnostics([{...error,message:"new",count:1}], [error]).length,1)
})
