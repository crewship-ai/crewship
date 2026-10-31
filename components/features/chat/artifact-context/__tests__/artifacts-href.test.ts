import { afterEach, expect, it, vi } from "vitest"
import { artifactsItemHref, chatItemHref } from "../artifacts-href"

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
