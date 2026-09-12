import { test } from "node:test"
import assert from "node:assert/strict"
import ts from "typescript"
import { compareDiagnostics, compareBaseline, diagnosticAnchor } from "./typecheck-tests.mjs"
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

test("same diagnostic at another code site fails", () => {
 assert.equal(compareDiagnostics([{...error,anchor:"new"}], [{...error,anchor:"old"}]).length,1)
})
test("raising the submitted baseline fails against the target branch", () => {
 assert.equal(compareBaseline([{...error,count:3,anchor:"site"}], [error]).length,1)
 assert.deepEqual(compareBaseline([{...error,anchor:"site"}], [error]),[])
 assert.equal(compareBaseline([{...error,anchor:"another"}], [{...error,anchor:"site"}]).length,1)
})

test("anchors distinguish declarations and test names but tolerate shifted lines", () => {
 const anchor = source => {
  const file=ts.createSourceFile("probe.test.ts",source,ts.ScriptTarget.Latest,true)
  return diagnosticAnchor({file,start:source.indexOf("bad")})
 }
 const source='it("first", () => { const bad: string = 1 })'
 assert.equal(anchor(source),anchor("\n\n"+source))
 assert.notEqual(anchor(source),anchor(source.replace("first","second")))
 assert.notEqual(anchor(source),anchor(source.replace("bad:","badOther:")))
 const helper='const first = () => { const bad: string = 1 }'
 assert.notEqual(anchor(helper),anchor(helper.replace("first","second")))
 const method='class First { run() { const bad: string = 1 } }'
 assert.notEqual(anchor(method),anchor(method.replace("First","Second")))
})
