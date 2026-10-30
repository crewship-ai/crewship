import { describe, expect, it } from "vitest"
import { sameProjectFiles } from "../use-artifact-project"

const body={base_version:4,entrypoint:"index.html",files:[{path:"index.html",content:"<h1>A</h1>",media_type:"text/html"},{path:"menu.html",content:"m",media_type:"text/html"}]}

describe("project save lost acknowledgement",()=>{
 it("confirms only the exact successor with identical files",()=>{
  expect(sameProjectFiles({version:5,entrypoint:"index.html",files:[body.files[1],body.files[0]]},body)).toBe(true)
 })
 it.each([
  ["a later version",{version:6,entrypoint:"index.html",files:body.files}],
  ["other content",{version:5,entrypoint:"index.html",files:[{...body.files[0],content:"<h1>B</h1>"},body.files[1]]}],
  ["a missing file",{version:5,entrypoint:"index.html",files:[body.files[0]]}],
  ["another entrypoint",{version:5,entrypoint:"menu.html",files:body.files}],
 ])("never claims %s",(_name,saved)=>{
  expect(sameProjectFiles(saved,body)).toBe(false)
 })
})
