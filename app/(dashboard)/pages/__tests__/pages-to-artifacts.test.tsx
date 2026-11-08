import { cleanup, render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { AccessMode } from "@/hooks/use-access-mode"

// /pages hands its overview to the Artifacts library whenever the library is
// on (#5129); a reference link resolves to its artifact. A restricted member
// moves to their Artifacts catalog by address alone, without a page read (#5132).
const nav = vi.hoisted(() => ({ params: new URLSearchParams("workspace_id=ws1"), replace: vi.fn(), access: "trusted" as AccessMode }))
vi.mock("next/navigation", () => ({ useSearchParams: () => nav.params, useRouter: () => ({ replace: nav.replace, push: vi.fn() }) }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => nav.access }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: () => <p>Pages overview</p> }))
vi.mock("@/components/features/pages/page-reference-entry", () => ({ PageReferenceEntry: () => <p>Reference</p> }))
vi.mock("@/components/features/artifacts-library/legacy-pages-redirect", () => ({
  LegacyPagesRedirect: ({ target, children }: { target: { reference: string; workspaceId: string }; children: ReactNode }) =>
    <div data-testid="resolve" data-reference={target.reference} data-ws={target.workspaceId}>{children}</div>,
}))

import PagesPage from "../page"

beforeEach(() => {
  nav.params = new URLSearchParams("workspace_id=ws1")
  nav.access = "trusted"
  nav.replace.mockReset()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("sends the Pages overview to the Artifacts library with the workspace, replacing the entry", () => {
  render(<PagesPage />)
  expect(nav.replace).toHaveBeenCalledWith("/artifacts?workspace_id=ws1")
  expect(screen.queryByText("Pages overview")).toBeNull()
})

it("stays on Pages when the library is off", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  render(<PagesPage />)
  expect(nav.replace).not.toHaveBeenCalled()
  expect(screen.getByText("Pages overview")).toBeTruthy()
})

it("resolves a reference link to its artifact, falling back to the Pages reference entry", () => {
  nav.params = new URLSearchParams("workspace_id=ws1&ref=abc")
  render(<PagesPage />)
  expect(screen.getByTestId("resolve").dataset).toMatchObject({ reference: "@abc", ws: "ws1" })
  expect(screen.getByText("Reference")).toBeTruthy()
})

it("keeps a reference link on the Pages reference entry without the library", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  nav.params = new URLSearchParams("workspace_id=ws1&ref=abc")
  render(<PagesPage />)
  expect(nav.replace).not.toHaveBeenCalled()
  expect(screen.queryByTestId("resolve")).toBeNull()
  expect(screen.getByText("Reference")).toBeTruthy()
})

it.each([
  ["", "/artifacts?workspace_id=ws1"],
  ["&ref=abc", "/artifacts/abc?workspace_id=ws1"],
])("moves a restricted member to their Artifacts catalog without a page read (query %s)", (extra, target) => {
  nav.access = "restricted"
  nav.params = new URLSearchParams(`workspace_id=ws1${extra}`)
  window.history.replaceState(null, "", `/pages?${nav.params}`)
  render(<PagesPage />)
  expect(nav.replace).toHaveBeenCalledWith(target)
  expect(screen.queryByTestId("resolve")).toBeNull()
  expect(screen.queryByText("Reference")).toBeNull()
  expect(screen.queryByText("Pages overview")).toBeNull()
})

it("waits for the access mode before deciding", () => {
  nav.access = "loading"
  render(<PagesPage />)
  expect(nav.replace).not.toHaveBeenCalled()
})
