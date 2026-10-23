import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve, join } from "node:path";
import { pathToFileURL } from "node:url";
const bindingsDir = resolve(process.argv[2]);
const wasm = await import(pathToFileURL(join(bindingsDir, "mooring_client.js")));
wasm.initSync({ module: readFileSync(join(bindingsDir, "mooring_client_bg.wasm")) });
const cases = JSON.parse(readFileSync(new URL("../fixtures/native-wasm.json", import.meta.url), "utf8"));
for (const value of cases) {
  assert.deepEqual(JSON.parse(wasm.conformance_case(JSON.stringify(value))), value.expected);
}
console.log(`${cases.length} shared vectors passed through optimized web-target WASM`);
