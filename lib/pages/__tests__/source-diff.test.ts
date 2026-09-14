import { describe, expect, it } from "vitest"
import { compareSources, decodeSourceFile } from "../source-diff"
import type { SourceProjectLike } from "../source-diff"
import { SOURCE_DIFF_LIMITS } from "@/lib/pages/editor-contract"

const project = (files: Array<{ path: string; encoding?: string; content: string }>): SourceProjectLike => ({ files })

const base64 = (bytes: number[]) => btoa(String.fromCharCode(...bytes))

const file = (path: string, content: string) => ({ path, content })

const lines = (count: number, mark = "") => Array.from({ length: count }, (_, i) => `line ${i}${mark}`).join("\n")

describe("decodeSourceFile", () => {
  const cases: Array<{ what: string; input: { encoding?: string; content: string }; text: string | null; bytes: number }> = [
    { what: "plain text with no encoding", input: { content: "hello\n" }, text: "hello\n", bytes: 6 },
    { what: "declared utf8", input: { encoding: "utf8", content: "héllo" }, text: "héllo", bytes: 6 },
    { what: "base64 text", input: { encoding: "base64", content: btoa("hello") }, text: "hello", bytes: 5 },
    { what: "base64 that is not UTF-8", input: { encoding: "base64", content: base64([0xff, 0xfe, 0x41]) }, text: null, bytes: 3 },
    { what: "base64 holding a NUL", input: { encoding: "base64", content: base64([0x41, 0x00, 0x42]) }, text: null, bytes: 3 },
    { what: "text holding a NUL", input: { content: "a\u0000b" }, text: null, bytes: 3 },
    { what: "an encoding the transport does not define", input: { encoding: "gzip", content: "AAAA" }, text: null, bytes: 4 },
  ]
  it.each(cases)("decodes $what", ({ input, text, bytes }) => {
    expect(decodeSourceFile(input)).toEqual({ text, bytes })
  })

  it("reports a size for base64 it cannot decode at all", () => {
    const decoded = decodeSourceFile({ encoding: "base64", content: "not!valid!base64!" })
    expect(decoded.text).toBeNull()
    expect(decoded.bytes).toBeGreaterThan(0)
  })
})

describe("file-level comparison", () => {
  it("classifies added, modified and removed, and omits identical files", () => {
    const before = project([file("src/App.tsx", "one\ntwo\n"), file("src/gone.ts", "x\n"), file("src/same.ts", "same\n")])
    const after = project([file("src/App.tsx", "one\ntwo!\n"), file("src/new.ts", "new\n"), file("src/same.ts", "same\n")])
    const diff = compareSources(before, after)
    expect(diff.files.map(entry => [entry.path, entry.status])).toEqual([
      ["src/App.tsx", "modified"],
      ["src/gone.ts", "removed"],
      ["src/new.ts", "added"],
    ])
    expect([diff.filesAdded, diff.filesModified, diff.filesRemoved]).toEqual([1, 1, 1])
    expect(diff.truncated).toBe(false)
  })

  it("reports a rename as a removal plus an addition, never as a move", () => {
    const body = "export const value = 1\n"
    const diff = compareSources(project([file("src/old.ts", body)]), project([file("src/new.ts", body)]))
    expect(diff.files.map(entry => [entry.path, entry.status])).toEqual([
      ["src/new.ts", "added"],
      ["src/old.ts", "removed"],
    ])
    expect([diff.filesAdded, diff.filesRemoved]).toEqual([1, 1])
  })

  it("treats a null side as everything added or everything removed", () => {
    const only = project([file("a.ts", "a\n"), file("b.ts", "b\n")])
    expect(compareSources(null, only).files.map(entry => entry.status)).toEqual(["added", "added"])
    expect(compareSources(only, null).files.map(entry => entry.status)).toEqual(["removed", "removed"])
    expect(compareSources(null, null).files).toEqual([])
    expect(compareSources(only, only).files).toEqual([])
  })

  it("treats the same bytes in a different encoding as unchanged", () => {
    const utf8 = project([{ path: "a.ts", encoding: "utf8", content: "hello\n" }])
    const b64 = project([{ path: "a.ts", encoding: "base64", content: btoa("hello\n") }])
    expect(compareSources(utf8, b64).files).toEqual([])
  })

  it("ignores malformed entries rather than throwing", () => {
    const junk = { files: [null, 7, { content: "no path" }, { path: "", content: "x" }, { path: "ok.ts", content: "ok\n" }] } as unknown as SourceProjectLike
    expect(() => compareSources(junk, junk)).not.toThrow()
    expect(compareSources(null, junk).files.map(entry => entry.path)).toEqual(["ok.ts"])
    expect(compareSources("nonsense" as unknown as SourceProjectLike, null).files).toEqual([])
  })
})

describe("line diff", () => {
  it("numbers both sides and keeps three lines of context", () => {
    const before = project([file("a.ts", "1\n2\n3\n4\n5\n6\n7\n8\n9\n")])
    const after = project([file("a.ts", "1\n2\n3\n4\nFOUR AND A HALF\n5\n6\n7\n8\n9\n")])
    const entry = compareSources(before, after).files[0]
    expect([entry.added, entry.removed, entry.binary, entry.truncated]).toEqual([1, 0, false, false])
    expect(entry.lines.map(line => [line.kind, line.text, line.oldLine, line.newLine])).toEqual([
      ["hunk", "@@ -2,6 +2,7 @@", null, null],
      ["context", "2", 2, 2],
      ["context", "3", 3, 3],
      ["context", "4", 4, 4],
      ["add", "FOUR AND A HALF", null, 5],
      ["context", "5", 5, 6],
      ["context", "6", 6, 7],
      ["context", "7", 7, 8],
    ])
  })

  it("renders a whole added file and a whole removed file", () => {
    const added = compareSources(null, project([file("a.ts", "one\ntwo\n")])).files[0]
    expect(added.added).toBe(2)
    expect(added.lines.filter(line => line.kind === "add").map(line => line.newLine)).toEqual([1, 2])
    const removed = compareSources(project([file("a.ts", "one\ntwo\n")]), null).files[0]
    expect(removed.removed).toBe(2)
    expect(removed.lines.filter(line => line.kind === "del").map(line => line.oldLine)).toEqual([1, 2])
  })
})

describe("limits", () => {
  it("describes a binary file without rendering a byte of it", () => {
    const before = project([{ path: "logo.png", encoding: "base64", content: base64([0x89, 0x50, 0x4e, 0x47]) }])
    const after = project([{ path: "logo.png", encoding: "base64", content: base64([0x89, 0x50, 0x4e, 0x48]) }])
    const entry = compareSources(before, after).files[0]
    expect(entry.binary).toBe(true)
    expect(entry.lines).toEqual([])
    expect([entry.added, entry.removed, entry.status]).toEqual([0, 0, "modified"])
  })

  it("lists a file over the byte cap without diffing it", () => {
    const big = `${"x".repeat(SOURCE_DIFF_LIMITS.bytesPerFile + 10)}\n`
    const diff = compareSources(project([file("big.txt", "small\n")]), project([file("big.txt", big)]))
    const entry = diff.files[0]
    expect(entry.lines).toEqual([])
    expect(entry.binary).toBe(false)
    expect(entry.truncated).toBe(true)
    // Upper-bound counts: a whole-file replace, never an understatement.
    expect([entry.added, entry.removed]).toEqual([1, 1])
    expect(diff.truncated).toBe(true)
  })

  it("stops rendering at the per-file line cap and says so", () => {
    const before = Array.from({ length: 1000 }, (_, i) => `line ${i}`)
    const after = before.map((line, i) => (i % 10 === 0 ? `${line} changed` : line))
    const diff = compareSources(project([file("a.ts", `${before.join("\n")}\n`)]), project([file("a.ts", `${after.join("\n")}\n`)]))
    const entry = diff.files[0]
    expect([entry.added, entry.removed]).toEqual([100, 100])
    expect(entry.lines.length).toBeLessThanOrEqual(SOURCE_DIFF_LIMITS.linesPerFile)
    expect(entry.truncated).toBe(true)
    expect(diff.truncated).toBe(true)
  })

  it("degrades to a whole-region replace when the edit budget is exhausted", () => {
    const before = lines(2000)
    const after = lines(2000, " rewritten")
    const entry = compareSources(project([file("a.ts", before)]), project([file("a.ts", after)])).files[0]
    expect(entry.added).toBe(2000)
    expect(entry.removed).toBe(2000)
    expect(entry.truncated).toBe(true)
  })

  it("does not run the LCS over a huge changed region, and still trims around a small edit", () => {
    // Short lines on purpose: 30 000 of them stay under the byte cap, so this
    // reaches the line guard rather than the oversize one.
    const before = Array.from({ length: 30_000 }, (_, i) => String(i % 10))
    const rewritten = before.map(line => `${line}x`)
    const wholesale = compareSources(project([file("a.ts", `${before.join("\n")}\n`)]), project([file("a.ts", `${rewritten.join("\n")}\n`)])).files[0]
    expect(wholesale.truncated).toBe(true)
    expect([wholesale.added, wholesale.removed]).toEqual([30_000, 30_000])

    const oneEdit = [...before]
    oneEdit[15_000] = "EDITED"
    const trimmed = compareSources(project([file("a.ts", `${before.join("\n")}\n`)]), project([file("a.ts", `${oneEdit.join("\n")}\n`)])).files[0]
    expect([trimmed.added, trimmed.removed]).toEqual([1, 1])
    expect(trimmed.truncated).toBe(false)
    expect(trimmed.lines[0].text).toBe("@@ -14998,7 +14998,7 @@")
  })

  it("gives bodies to the first filesWithBodies files and a status to the rest", () => {
    const total = SOURCE_DIFF_LIMITS.filesWithBodies + 5
    const before = project(Array.from({ length: total }, (_, i) => file(`src/f${String(i).padStart(3, "0")}.ts`, "one\n")))
    const after = project(Array.from({ length: total }, (_, i) => file(`src/f${String(i).padStart(3, "0")}.ts`, "two\n")))
    const diff = compareSources(before, after)
    expect(diff.files).toHaveLength(total)
    expect(diff.files.filter(entry => entry.lines.length > 0)).toHaveLength(SOURCE_DIFF_LIMITS.filesWithBodies)
    for (const entry of diff.files.slice(SOURCE_DIFF_LIMITS.filesWithBodies)) {
      expect(entry.lines).toEqual([])
      expect(entry.truncated).toBe(true)
      // The counts survive, so a body-less row is still a described change.
      expect([entry.added, entry.removed]).toEqual([1, 1])
    }
    expect(diff.truncated).toBe(true)
  })
})
