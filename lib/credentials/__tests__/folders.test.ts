import { describe, expect, it } from "vitest"

import { applyCredentialFilters, needsAttention, EMPTY_CREDENTIAL_FILTERS, type CredentialLike } from "../facets"
import { buildFolderTree, folderIsRestricted, folderPathLabel, folderSelectionIds, type CredentialFolder } from "../folders"
import { buildVaultQuery, hasExplicitVaultState, parseVaultUrl, activeFacetCount } from "../vault-url-state"

const f = (id: string, name: string, parent: string | null = null, over: Partial<CredentialFolder> = {}): CredentialFolder => ({
  id, name, parent_id: parent, description: "", reveal_policy: "inherit", remind_after_days: 0,
  default_security_level: null, min_security_level: 1, credential_count: 0, ...over,
})
const FOLDERS = [f("prod", "Production", null, { credential_count: 2 }), f("pay", "Payments", "prod", { credential_count: 1 }), f("ops", "Ops"), f("stg", "Staging")]

const cred = (id: string, over: Partial<CredentialLike> = {}): CredentialLike => ({
  id, name: id, provider: "GITHUB", status: "ACTIVE", scope: "WORKSPACE", last_used_at: new Date().toISOString(), ...over,
})

describe("folder helpers", () => {
  it("lists roots A-Z, each followed by its subfolders, with the subtree total", () => {
    const tree = buildFolderTree(FOLDERS)
    expect(tree.map((n) => [n.folder.name, n.depth, n.total])).toEqual([
      ["Ops", 0, 0], ["Production", 0, 3], ["Payments", 1, 1], ["Staging", 0, 0],
    ])
  })
  it("a selected root covers its subfolders, a subfolder only itself", () => {
    expect([...folderSelectionIds(FOLDERS, ["prod"])].sort()).toEqual(["pay", "prod"])
    expect([...folderSelectionIds(FOLDERS, ["pay"])]).toEqual(["pay"])
  })
  it("names a subfolder with its parent", () => {
    expect(folderPathLabel(FOLDERS, "pay")).toBe("Production / Payments")
    expect(folderPathLabel(FOLDERS, null)).toBe("")
  })
  it("a reveal policy or tier floor makes a folder restricted", () => {
    expect(folderIsRestricted(f("a", "A"))).toBe(false)
    expect(folderIsRestricted(f("a", "A", null, { reveal_policy: "admins" }))).toBe(true)
    expect(folderIsRestricted(f("a", "A", null, { min_security_level: 3 }))).toBe(true)
  })
})

describe("folder filter", () => {
  const rows = [cred("a", { folder_id: "prod" }), cred("b", { folder_id: "pay" }), cred("c"), cred("d", { folder_id: "ops" })]
  const run = (folder: string[]) =>
    applyCredentialFilters(rows, { ...EMPTY_CREDENTIAL_FILTERS, folder }, new Set(), { folderIds: folderSelectionIds(FOLDERS, folder) }).map((c) => c.id)

  it("narrows to the folder and, for a root, its subfolders", () => {
    expect(run([])).toEqual(["a", "b", "c", "d"])
    expect(run(["prod"])).toEqual(["a", "b"])
    expect(run(["pay"])).toEqual(["b"])
    expect(run(["ops", "pay"])).toEqual(["b", "d"])
  })
  it("round-trips through the address bar and counts as a facet", () => {
    const state = parseVaultUrl(new URLSearchParams("folder=prod"))
    expect(state.filters.folder).toEqual(["prod"])
    expect(buildVaultQuery(state)).toBe("?folder=prod")
    expect(hasExplicitVaultState(new URLSearchParams("folder=prod"))).toBe(true)
    expect(activeFacetCount(state.filters)).toBe(1)
  })
})

describe("replace reminder", () => {
  it("a secret past its folder's reminder needs attention", () => {
    expect(needsAttention(cred("x", { replace_due: true }))).toBe(true)
    expect(needsAttention(cred("x", { replace_due: false }))).toBe(false)
  })
})
