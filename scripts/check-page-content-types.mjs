// Run from repository root through crewship-heavy; requires Node 24 and Go.
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { execFileSync } from "node:child_process"
import { contentTypes, deriveContentTypes } from "../lib/pages/content-types.ts"
const fixtures = JSON.parse(readFileSync("testdata/page-content-derivation.json", "utf8"))
const actualGo = JSON.parse(execFileSync("go", ["run", "./scripts/page-content-contract"], { encoding: "utf8" }))
const actualTS = { registry: contentTypes(), results: fixtures.map(({ facts }) => deriveContentTypes(facts)) }
assert.deepEqual(actualGo, actualTS, "actual Go/TS registry and derivation differ")
assert.deepEqual(actualTS.results, fixtures.map(({ expected }) => expected), "shared contract differs")
console.log(`Page content contract: ${actualTS.registry.length} descriptors, ${fixtures.length} Go/TS derivations agree`)
