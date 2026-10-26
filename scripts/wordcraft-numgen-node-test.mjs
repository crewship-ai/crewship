import { test } from "node:test"
import assert from "node:assert/strict"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import { spawnSync } from "node:child_process"
import { fileURLToPath } from "node:url"
import vm from "node:vm"
import crypto from "node:crypto"

const generator = fileURLToPath(new URL("../mooring/vendor/serde_json_canonicalizer-0.4.1/tests/resources/generated-numbers/numgen.js", import.meta.url))
const outputName = "es6testfile100m.txt"
const retained = Buffer.from("Existing numeric fixture must remain intact.\n")
function fixture(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "wordcraft-numgen-"))
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  return { directory, output: path.join(directory, outputName) }
}

test("the actual generator refuses existing output without modifying it", t => {
  const { directory, output } = fixture(t)
  fs.writeFileSync(output, retained)
  // The vendored program is CommonJS; stdin avoids the product root ESM scope.
  const result = spawnSync(process.execPath, ["--input-type=commonjs", "-"], { input: fs.readFileSync(generator), cwd: directory, encoding: "utf8", timeout: 5_000 })
  assert.ifError(result.error)
  assert.equal(result.status, 1)
  assert.match(result.stdout, /already exists/)
  assert.deepEqual(fs.readFileSync(output), retained)
})

test("a competing create immediately before the real open cannot truncate output", t => {
  const { output } = fixture(t)
  const exited = new Error("generator exited")
  let exitCode, opened = false
  // Execute the unchanged generator bytes. Inject only a deterministic competing
  // filesystem create at open, then use the real kernel open with its actual flags.
  const observedFS = {
    ...fs,
    existsSync: () => fs.existsSync(output),
    openSync(_filename, flags) {
      fs.writeFileSync(output, retained, { flag: "wx" })
      const descriptor = fs.openSync(output, flags)
      fs.closeSync(descriptor)
      opened = true
      throw Error("stop before the 100-million-line generation loop")
    },
  }
  try {
    vm.runInNewContext(fs.readFileSync(generator, "utf8"), {
      require(name) { if (name === "fs") return observedFS; if (name === "crypto") return crypto; throw Error("unexpected generator import") },
      process: { exit(code) { exitCode = code; throw exited } },
      console: { log() {} }, Buffer,
    }, { filename: generator, timeout: 5_000 })
  } catch (error) { if (error !== exited && !opened) throw error }
  assert.deepEqual(fs.readFileSync(output), retained)
  assert.equal(opened, false, "the competitor must retain exclusive ownership")
  assert.equal(exitCode, 1)
})
