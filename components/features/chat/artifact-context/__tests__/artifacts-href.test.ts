import { afterEach, expect, it, vi } from "vitest"
import { artifactsEditHref, artifactsItemHref, artifactsRefHref, chatItemHref } from "../artifacts-href"

afterEach(() => { vi.unstubAllEnvs() })

it("keeps the Pages route while the library is off", () => {
  expect(chatItemHref("ws", "p 1", "/pages/@p%201")).toBe("/pages/@p%201")
})

it("sends every chat link to the item's own Artifacts address with the library on", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  expect(chatItemHref("ws 1", "p/1", "/pages/@p%2F1")).toBe("/artifacts/p%2F1?workspace_id=ws+1")
  expect(chatItemHref(null, "p1", "/pages/@p1")).toBe("/artifacts/p1")
  expect(artifactsItemHref("ws", "p1")).toBe("/artifacts/p1?workspace_id=ws")
})

it.each<[string, string | null, string, string]>([
  ["an @<id> reference", "ws 1", "@page-1", "/artifacts/page-1?workspace_id=ws+1"],
  ["a slug", "ws1", "q&a plan", "/artifacts?item=q%26a+plan&workspace_id=ws1"],
  ["a slug without a workspace", null, "brief", "/artifacts?item=brief"],
])("opens %s in Artifacts, never on /pages", (_name, workspace, reference, href) => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  expect(artifactsRefHref(workspace, reference, "/pages/x")).toBe(href)
})

it("opens the editor inside the item's Artifacts frame", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  expect(artifactsEditHref("ws 1", "page/1", "/pages/x?edit=1")).toBe("/artifacts/page%2F1?workspace_id=ws+1&edit=1")
})

it("keeps the Pages address of a reference and an editor only in a build with the library off", () => {
  expect(artifactsRefHref("ws", "@p1", "/pages/%40p1")).toBe("/pages/%40p1")
  expect(artifactsEditHref("ws", "p1", "/pages/%40p1?artifact=1&edit=1")).toBe("/pages/%40p1?artifact=1&edit=1")
})
