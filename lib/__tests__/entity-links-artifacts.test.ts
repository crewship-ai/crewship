import { afterEach, expect, it, vi } from "vitest"
import { entityHref } from "@/lib/entity-links"
import { refHref } from "@/lib/entity-refs"
import { entityHref as panelEntityHref } from "@/components/features/pages/panels/entity-href"

// Owner, 8 Nov 2026: saved work lives only in Artifacts. With the library on
// (every checked-in build profile) no cross-link points at /pages.
afterEach(() => vi.unstubAllEnvs())

it.each<[string, () => string | null, string]>([
  ["an entity link", () => entityHref({ kind: "page", slug: "q4 plan" }), "/artifacts?item=q4%20plan"],
  ["a stored page/ ref", () => refHref("page/watch"), "/artifacts?item=watch"],
  ["a panel's entity ref", () => panelEntityHref({ kind: "page", id: "weekly-report" }), "/artifacts?item=weekly-report"],
])("opens %s in Artifacts", (_name, href, expected) => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  expect(href()).toBe(expected)
})

it("keeps the Pages address only in a build with the library off", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  expect(entityHref({ kind: "page", slug: "watch" })).toBe("/pages/watch")
  expect(panelEntityHref({ kind: "page", id: "watch" })).toBe("/pages/watch")
})
