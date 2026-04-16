import { describe, it, expect } from "vitest"
import { isValidElement } from "react"
import {
  buildTopLevelTree,
  insertTreeChildren,
  findTreeNode,
  isPreviewable,
  getEditorLanguage,
  getChatFileIcon,
  type FileEntry,
  type TreeNode,
} from "../chat-tree-row"

function file(partial: Partial<FileEntry> & { name: string; is_dir: boolean }): FileEntry {
  return {
    path: partial.path ?? partial.name,
    name: partial.name,
    size: partial.size ?? 0,
    is_dir: partial.is_dir,
    mod_time: partial.mod_time ?? "2026-04-17T00:00:00Z",
  }
}

describe("buildTopLevelTree", () => {
  it("sorts directories before files", () => {
    const tree = buildTopLevelTree([
      file({ name: "readme.md", is_dir: false }),
      file({ name: "src", is_dir: true }),
      file({ name: "package.json", is_dir: false }),
      file({ name: "docs", is_dir: true }),
    ])
    expect(tree.map((n) => n.name)).toEqual(["docs", "src", "package.json", "readme.md"])
  })

  it("sorts alphabetically within each group", () => {
    const tree = buildTopLevelTree([
      file({ name: "zeta", is_dir: true }),
      file({ name: "alpha", is_dir: true }),
      file({ name: "mu", is_dir: true }),
    ])
    expect(tree.map((n) => n.name)).toEqual(["alpha", "mu", "zeta"])
  })

  it("initialises children to [] and sets childrenLoaded according to is_dir", () => {
    const tree = buildTopLevelTree([
      file({ name: "dir", is_dir: true }),
      file({ name: "file.ts", is_dir: false }),
    ])
    const [dir, leaf] = tree
    expect(dir.children).toEqual([])
    expect(dir.childrenLoaded).toBe(false) // dirs are loaded lazily
    expect(leaf.children).toEqual([])
    expect(leaf.childrenLoaded).toBe(true) // files need no further load
  })
})

describe("insertTreeChildren", () => {
  function makeTree(): TreeNode[] {
    return buildTopLevelTree([
      file({ path: "src", name: "src", is_dir: true }),
      file({ path: "docs", name: "docs", is_dir: true }),
      file({ path: "readme.md", name: "readme.md", is_dir: false }),
    ])
  }

  it("inserts sorted children into the matching directory and flips childrenLoaded", () => {
    const tree = makeTree()
    const next = insertTreeChildren(tree, "src", [
      file({ path: "src/zeta.ts", name: "zeta.ts", is_dir: false }),
      file({ path: "src/components", name: "components", is_dir: true }),
      file({ path: "src/alpha.ts", name: "alpha.ts", is_dir: false }),
    ])

    const src = next.find((n) => n.path === "src")!
    expect(src.childrenLoaded).toBe(true)
    expect(src.children.map((c) => c.name)).toEqual(["components", "alpha.ts", "zeta.ts"])
  })

  it("recurses into subdirectories to find the parent path", () => {
    let tree = makeTree()
    tree = insertTreeChildren(tree, "src", [
      file({ path: "src/nested", name: "nested", is_dir: true }),
    ])
    tree = insertTreeChildren(tree, "src/nested", [
      file({ path: "src/nested/deep.ts", name: "deep.ts", is_dir: false }),
    ])

    const deep = tree.find((n) => n.path === "src")!.children.find((c) => c.path === "src/nested")!
    expect(deep.children.map((c) => c.name)).toEqual(["deep.ts"])
  })

  it("returns a tree untouched when no path matches", () => {
    const tree = makeTree()
    const next = insertTreeChildren(tree, "nonexistent", [
      file({ path: "nonexistent/x", name: "x", is_dir: false }),
    ])
    expect(next).toEqual(tree)
  })
})

describe("findTreeNode", () => {
  const tree: TreeNode = {
    path: "root",
    name: "root",
    size: 0,
    is_dir: true,
    children: [
      {
        path: "root/a",
        name: "a",
        size: 0,
        is_dir: true,
        children: [
          { path: "root/a/file.ts", name: "file.ts", size: 1, is_dir: false, children: [] },
        ],
      },
      { path: "root/b", name: "b", size: 0, is_dir: false, children: [] },
    ],
  }

  it("returns the node when path matches the root", () => {
    expect(findTreeNode(tree, "root")?.name).toBe("root")
  })

  it("returns a nested descendant", () => {
    expect(findTreeNode(tree, "root/a/file.ts")?.name).toBe("file.ts")
  })

  it("returns undefined when path is absent", () => {
    expect(findTreeNode(tree, "root/missing")).toBeUndefined()
  })
})

describe("isPreviewable", () => {
  it("recognises common previewable source and text extensions", () => {
    for (const name of ["foo.ts", "foo.tsx", "foo.py", "foo.md", "foo.json", "foo.yaml", "foo.log"]) {
      expect(isPreviewable(name)).toBe(true)
    }
  })

  it("rejects binary and unknown extensions", () => {
    for (const name of ["foo.png", "foo.zip", "foo", "foo.exe", "foo.bin"]) {
      expect(isPreviewable(name)).toBe(false)
    }
  })

  it("is case-insensitive on the extension", () => {
    expect(isPreviewable("README.MD")).toBe(true)
    expect(isPreviewable("Script.SH")).toBe(true)
  })
})

describe("getEditorLanguage", () => {
  const cases: Array<[string, string]> = [
    ["foo.ts", "typescript"],
    ["foo.tsx", "tsx"],
    ["foo.js", "javascript"],
    ["foo.jsx", "jsx"],
    ["foo.py", "python"],
    ["foo.go", "go"],
    ["foo.yml", "yaml"],
    ["foo.yaml", "yaml"],
    ["foo.md", "markdown"],
    ["unknown.xyz", "text"],
    ["noext", "text"],
  ]

  for (const [name, lang] of cases) {
    it(`maps ${name} → ${lang}`, () => {
      expect(getEditorLanguage(name)).toBe(lang)
    })
  }
})

describe("getChatFileIcon", () => {
  it("returns an open folder element for a directory opened", () => {
    const el = getChatFileIcon("src", true, true)
    expect(isValidElement(el)).toBe(true)
  })

  it("returns a closed folder element for a directory not opened", () => {
    const el = getChatFileIcon("src", true, false)
    expect(isValidElement(el)).toBe(true)
  })

  it("returns a React element for a file with any extension", () => {
    const el = getChatFileIcon("module.ts", false)
    expect(isValidElement(el)).toBe(true)
  })

  it("falls back to a generic file icon for unknown extensions", () => {
    const el = getChatFileIcon("mystery.xyz", false)
    expect(isValidElement(el)).toBe(true)
  })
})
