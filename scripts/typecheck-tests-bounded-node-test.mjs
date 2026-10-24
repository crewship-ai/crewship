import {test} from "node:test"
import assert from "node:assert/strict"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import {checkTestTypes,checkTestTypesBounded} from "./typecheck-tests.mjs"

function project(t,files,options={}) {
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),"test-types-bounded-"))
 t.after(()=>fs.rmSync(dir,{recursive:true,force:true}))
 fs.writeFileSync(path.join(dir,"tsconfig.json"),JSON.stringify({compilerOptions:{strict:true,noEmit:true,types:[],skipLibCheck:true,target:"ES2020",jsx:"preserve",...options},include:["**/*.test.ts","**/*.test.tsx","globals.d.ts"]}))
 for(const[name,source]of Object.entries(files))fs.writeFileSync(path.join(dir,name),source)
 return path.join(dir,"tsconfig.json")
}

for(const[name,files,options]of[
 ["clean",{"one.test.ts":"export const n: number = 1"}],
 ["imported product error",{"one.test.ts":"import './product'; export {}","product.ts":"export const value: string = 1"}],
 ["cross-root globals",{"one.test.ts":"interface Crew { name: string }","two.test.ts":"interface Crew { count: number }","three.test.ts":"const crew: Crew = {name: 'owned'}"}],
 ["cross-root declaration conflict",{"one.test.ts":"const shared = 1","two.test.ts":"const shared = 2"}],
 ["required JSX props",{"one.test.tsx":"import {Widget} from './product'; const x = <Widget />","product.tsx":"export function Widget(props: {required: string}) { return null }"}],
 ["declaration diagnostics",{"one.test.ts":"export const factory = () => class {private value = 1}"},{declaration:true}],
 ["checked dts",{"one.test.ts":"export {}","globals.d.ts":"declare const value: MissingType"},{skipLibCheck:false}],
 ["unchecked dts remains global context",{"one.test.ts":"const value: Shared = {}","globals.d.ts":"interface Shared { required: string }"}],
 ["options diagnostics",{"one.test.ts":"export {}"},{jsx:"react",jsxImportSource:"react"}],
])test(`bounded matches full checker: ${name}`,t=>{
 const config=project(t,files,options)
 const reference=checkTestTypes(config)
 if(name!=="clean")assert.ok(reference.length>0,"fixture must expose an actual diagnostic")
 assert.deepEqual(checkTestTypesBounded(config,1),reference)
})

test("bad configuration and invalid batch fail closed",t=>{
 const config=project(t,{"one.test.ts":"export {}"})
 assert.throws(()=>checkTestTypesBounded(config,0))
 fs.writeFileSync(config,"{broken")
 assert.throws(()=>checkTestTypesBounded(config,1),/worker failed/)
})
