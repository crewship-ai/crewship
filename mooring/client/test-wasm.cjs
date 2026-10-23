// Execute the same committed cases as the native Rust tests through WASM.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const wasm = require(path.resolve(process.argv[2]));
const cases = JSON.parse(fs.readFileSync(path.join(__dirname, '../fixtures/native-wasm.json'), 'utf8'));
for (const value of cases) {
  assert.deepEqual(JSON.parse(wasm.conformance_case(JSON.stringify(value))), value.expected);
}
console.log(`${cases.length} native/WASM shared vectors passed through actual WASM ABI`);
