// Files a routine runs, as the tree the card draws.
//
// The server's `files` projection is flat (one row per declared path). The
// card draws it under /crew/shared/ as folders and files, the same way the
// agent Files panel does, and it must survive an older server that sends no
// `files` at all — the recipe's own script paths are the fallback.

import { describe, it, expect } from "vitest"
import { buildRoutineFileTree, routineFilesFromDefinition, fileLanguage, formatFileSize, type RoutineFile } from "../routine-files"

const file = (path: string, over: Partial<RoutineFile> = {}): RoutineFile => ({ path, language: fileLanguage(path), step_ids: [], present: true, ...over })

describe("buildRoutineFileTree", () => {
  it("nests four paths in three folders, folders first, and counts files per folder", () => {
    const tree = buildRoutineFileTree([
      file("scripts/ledger-post.go", { size_bytes: 4120 }),
      file("scripts/normalize_lines.py"),
      file("checks/invoice-rules.yaml"),
      file("prompts/docs-judge.md"),
      file("README.md"),
    ])
    expect(tree.map((n) => [n.name, n.is_dir])).toEqual([
      ["checks", true],
      ["prompts", true],
      ["scripts", true],
      ["README.md", false],
    ])
    const scripts = tree.find((n) => n.name === "scripts")!
    expect(scripts.children.map((c) => c.name)).toEqual(["ledger-post.go", "normalize_lines.py"])
    expect(scripts.fileCount).toBe(2)
    expect(scripts.children[0].file?.size_bytes).toBe(4120)
  })
  it("nests deeper folders and counts every descendant", () => {
    const tree = buildRoutineFileTree([file("checks/billing/health.sh"), file("checks/billing/lint.sh"), file("checks/ledger/health.sh")])
    const checks = tree[0]
    expect(checks.fileCount).toBe(3)
    expect(checks.children.map((c) => [c.name, c.fileCount])).toEqual([["billing", 2], ["ledger", 1]])
  })
  it("strips a leading /crew/shared/ so the tree has one root", () => {
    const tree = buildRoutineFileTree([file("/crew/shared/scripts/a.py")])
    expect(tree[0].name).toBe("scripts")
    expect(tree[0].children[0].path).toBe("scripts/a.py")
  })
})

describe("routineFilesFromDefinition", () => {
  it("derives files from the recipe when the server sends none, marking presence unknown", () => {
    const files = routineFilesFromDefinition({
      steps: [{ id: "post", type: "script", script: { path: "scripts/ledger-post.go", interpreter: "go" } }],
    })
    expect(files).toEqual([{ path: "scripts/ledger-post.go", language: "go", interpreter: "go", step_ids: ["post"], present: undefined, status: "unverified" }])
  })
})

describe("helpers", () => {
  it("names languages by extension and formats sizes", () => {
    expect(fileLanguage("a.go")).toBe("go")
    expect(fileLanguage("a.yml")).toBe("yaml")
    expect(fileLanguage("Makefile")).toBe("")
    expect(formatFileSize(4120)).toBe("4.0 kB")
    expect(formatFileSize(614)).toBe("614 B")
    expect(formatFileSize(undefined)).toBe("")
  })
})
