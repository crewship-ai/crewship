import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { AccessMode } from "@/hooks/use-access-mode"

// /pages hands its overview to the Artifacts library behind its own switch;
// reference links, restricted members and single pages stay on Pages.
const nav = vi.hoisted(() => ({ params: new URLSearchParams("workspace_id=ws1"), replace: vi.fn(), access: "trusted" as AccessMode }))
vi.mock("next/navigation", () => ({ useSearchParams: () => nav.params, useRouter: () => ({ replace: nav.replace, push: vi.fn() }) }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => nav.access }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/pages/restricted-pages", () => ({ RestrictedPages: () => <p>Restricted pages</p> }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <p>Pages overview</p> }))
vi.mock("@/components/features/pages/page-reference-entry", () => ({ PageReferenceEntry: () => <p>Reference</p> }))

import PagesPage from "../page"

beforeEach(() => {
  nav.params = new URLSearchParams("workspace_id=ws1")
  nav.access = "trusted"
  nav.replace.mockReset()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "true")
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("sends the Pages overview to the Artifacts library with the workspace, replacing the entry", () => {
  render(<PagesPage />)
  expect(nav.replace).toHaveBeenCalledWith("/artifacts?workspace_id=ws1")
  expect(screen.queryByText("Pages overview")).toBeNull()
})

it.each([
  ["the switch is off", () => vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "false")],
  ["the library itself is off", () => vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")],
])("stays on Pages when %s", (_name, setup) => {
  setup()
  render(<PagesPage />)
  expect(nav.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages overview")).toBeTruthy()
})

it("keeps a reference link on Pages", () => {
  nav.params = new URLSearchParams("workspace_id=ws1&ref=abc")
  render(<PagesPage />)
  expect(nav.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Reference")).toBeTruthy()
})

it("keeps restricted members on their Pages catalog", () => {
  nav.access = "restricted"
  render(<PagesPage />)
  expect(nav.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Restricted pages")).toBeTruthy()
})

it("waits for the access mode before deciding", () => {
  nav.access = "loading"
  render(<PagesPage />)
  expect(nav.replace).not.toHaveBeenCalled()
})
