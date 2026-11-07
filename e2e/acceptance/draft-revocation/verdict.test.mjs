import assert from "node:assert/strict"
import { test } from "node:test"
import { deflateSync } from "node:zlib"
import { compareImages, decodePng, measureImage } from "./pixels.mjs"
import { EXIT, missingEngines, nativeSettings, verdict } from "./verdict.mjs"

const passed = (n) => Array.from({ length: n }, () => ({ result: "passed" }))

test("every listed test passed", () => {
  assert.equal(verdict({ playwrightStatus: 0, listed: 2, rows: passed(2), narrowed: false }).exit, EXIT.pass)
})
test("one skip fails the run however green the rest is", () => {
  const v = verdict({ playwrightStatus: 0, listed: 3, rows: [...passed(2), { result: "skipped" }], narrowed: false })
  assert.equal(v.exit, EXIT.failed)
  assert.deepEqual(v.reasons, ["1 skipped"])
})
test("a test that left no result fails", () => {
  assert.equal(verdict({ playwrightStatus: 0, listed: 3, rows: passed(2), narrowed: false }).exit, EXIT.failed)
})
test("nothing listed is not a pass", () => {
  assert.equal(verdict({ playwrightStatus: 0, listed: 0, rows: [], narrowed: false }).exit, EXIT.failed)
})
test("a playwright failure with green rows fails", () => {
  assert.equal(verdict({ playwrightStatus: 1, listed: 1, rows: passed(1), narrowed: false }).exit, EXIT.failed)
})
test("a narrowed green run is partial, never a pass", () => {
  assert.equal(verdict({ playwrightStatus: 0, listed: 2, rows: passed(2), narrowed: true }).exit, EXIT.partial)
})
test("only native settings pass through, and agents stay off", () => {
  const settings = nativeSettings("# comment\nCREWSHIP_CRAFT_NATIVE_ENABLED=true\nCREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED=true\nCREWSHIP_PAGE_STUDIO_ORIGIN=https://example.invalid\n")
  assert.deepEqual([...settings.keys()], ["CREWSHIP_CRAFT_NATIVE_ENABLED"])
})
test("a missing engine is named", () => {
  const present = new Map([["CREWSHIP_CRAFT_NATIVE_ENABLED", "true"], ["CREWSHIP_CRAFT_NATIVE_EDITOR_BUNDLE_DIR", "/b"], ...["ENGINE_REVISION", "CLI_PATH", "CLI_SHA256", "EDITOR_BUNDLE", "EDITOR_URL"].map((n) => [`CREWSHIP_CRAFT_NATIVE_WORD_${n}`, "x"])])
  assert.equal(missingEngines(present).length, 5)
  assert.ok(missingEngines(present).every((name) => name.includes("_GRID_")))
})

function png(width, height, pixel, filter = 0) {
  const chunk = (kind, body) => { const head = Buffer.alloc(8); head.writeUInt32BE(body.length); head.write(kind, 4, "latin1"); return Buffer.concat([head, body, Buffer.alloc(4)]) }
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(width, 0); ihdr.writeUInt32BE(height, 4); ihdr[8] = 8; ihdr[9] = 2
  const rows = []
  for (let y = 0; y < height; y++) {
    const row = [filter]
    for (let x = 0; x < width; x++) {
      const now = pixel(x, y), up = y ? pixel(x, y - 1) : [0, 0, 0]
      row.push(...(filter === 2 ? now.map((v, i) => (v - up[i]) & 255) : now))
    }
    rows.push(Buffer.from(row))
  }
  return Buffer.concat([Buffer.alloc(8), chunk("IHDR", ihdr), chunk("IDAT", deflateSync(Buffer.concat(rows))), chunk("IEND", Buffer.alloc(0))])
}
const white = () => [255, 255, 255]
const text = (x, y) => x > 2 && x < 8 && y === 3 ? [0, 0, 0] : [255, 255, 255]
test("one colour is no content, in any colour", () => {
  assert.equal(measureImage(decodePng(png(10, 10, white))).contentShare, 0)
  assert.equal(measureImage(decodePng(png(10, 10, () => [0, 0, 0]))).contentShare, 0)
})
test("text counts as content, and each row filter decodes to the same pixels", () => {
  assert.equal(measureImage(decodePng(png(10, 10, text))).contentShare, 0.05)
  assert.deepEqual(decodePng(png(10, 10, text, 2)).data, decodePng(png(10, 10, text)).data)
})
test("content that went away counts as lost, and the same image does not", () => {
  assert.equal(compareImages(decodePng(png(10, 10, text)), decodePng(png(10, 10, white))).lost, 1)
  assert.equal(compareImages(decodePng(png(10, 10, text)), decodePng(png(10, 10, text))).lost, 0)
})
test("a different size is lost content", () => {
  assert.equal(compareImages(decodePng(png(10, 10, text)), decodePng(png(9, 10, text))).lost, 1)
})
