import { describe, expect, it } from "vitest"
import { editorRouteHref, readEditorRoute } from "../editor-contract"
import { readStablePageReference, resolvedPageIdentity, stablePageHref } from "../stable-links"

describe("whole-Page identity links", () => {
  it("carries explicit workspace and original identity without a mutable slug", () => {
    expect(stablePageHref("workspace-A", "page-original")).toBe("/pages?ref=page-original&workspace_id=workspace-A")
    expect(readStablePageReference("?ref=page-original&workspace_id=workspace-A")).toEqual({ kind: "page", pageId: "page-original", workspaceId: "workspace-A" })
    expect(readStablePageReference("?workspace_id=workspace-A")).toBeUndefined()
  })
  it("refuses missing, duplicate and malformed identity pairs instead of falling back to a slug hint", () => {
    for (const query of ["?ref=page", "?ref=", "?ref=page&workspace_id=ws&ref=other", "?ref=page&workspace_id=ws&workspace_id=other", "?ref=../page&workspace_id=ws", "?ref=page&workspace_id=ws%2Fother"]) {
      expect(readEditorRoute("/pages/reused-address", query).reference).toEqual({ kind: "invalid" })
    }
  })
  it("retains the expected identity across canonicalization and editor mode changes", () => {
    const route = readEditorRoute("/pages/old-address", "?ref=original&workspace_id=ws&mode=edit&tab=alerts")
    const current = editorRouteHref({ ...route, slug: "new-address" }, "?ref=other&workspace_id=foreign&tab=alerts")
    expect(current).toBe("/pages/new-address?tab=alerts&ref=original&workspace_id=ws&mode=edit")
    expect(readEditorRoute("/pages/new-address", current.slice(current.indexOf("?"))).reference).toEqual(route.reference)
  })
  it("clears reference intent only for an intentional new Page selection", () => {
    expect(editorRouteHref({ slug: "another", mode: "view", section: "content", pane: "section" }, "?ref=original&workspace_id=ws&tab=alerts")).toBe("/pages/another?tab=alerts")
    expect(editorRouteHref({ slug: "legacy", mode: "view", section: "content", pane: "section" }, "?workspace_id=legacy-context")).toBe("/pages/legacy?workspace_id=legacy-context")
  })
  it("refuses a resolver response for another ID or an executable/noncanonical address", () => {
    expect(resolvedPageIdentity({ page_id: "original", slug: "renamed-page" }, "original")).toEqual({ page_id: "original", slug: "renamed-page" })
    for (const value of [{ page_id: "different", slug: "old-address" }, { page_id: "original", slug: "//outside.test" }, { page_id: "original", slug: "../other" }]) {
      expect(() => resolvedPageIdentity(value, "original")).toThrow("Page link is unavailable")
    }
  })
})
