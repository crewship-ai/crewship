import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

const state = vi.hoisted(() => ({ search: "workspace_id=ws1", slug: "weekly-report" as string | null, access: "trusted", replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.search), useRouter: () => ({ replace: state.replace }) }))
vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => state.slug }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => state.access }))
vi.mock("@/components/features/restricted/access-notice", () => ({ useAccessContext: () => ({}) }))
vi.mock("@/components/features/pages/restricted-pages", () => ({ RestrictedPages: () => <div data-testid="restricted" /> }))
vi.mock("@/components/features/pages/pages-layout", () => ({ PagesLayout: ({ slug }: { slug: string }) => <div data-testid="pages" data-slug={slug} /> }))
vi.mock("@/components/features/pages/artifact-project", () => ({ ArtifactProject: () => <div data-testid="project" /> }))

import { PageDetailClient } from "../page-client"

const on = () => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true"); vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "true") }
beforeEach(() => { state.search = "workspace_id=ws1"; state.slug = "weekly-report"; state.access = "trusted"; state.replace.mockReset() })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("moves a saved /pages/<slug> link to the artifact inside Artifacts, without a Back step to Pages", () => {
  on()
  render(<PageDetailClient />)
  expect(state.replace).toHaveBeenCalledWith("/artifacts?workspace_id=ws1&item=weekly-report")
  expect(screen.queryByTestId("pages")).toBeNull()
})

it("opens an immutable reference, and a project link, at the artifact's ID", () => {
  on()
  state.slug = "@page-weekly"; state.search = "workspace_id=ws1&artifact=1&version=2"
  render(<PageDetailClient />)
  expect(state.replace).toHaveBeenCalledWith("/artifacts/page-weekly?workspace_id=ws1")
  expect(screen.queryByTestId("project")).toBeNull()
})

it.each([
  ["a reference link", "ref=abc"],
  ["the panel editor", "mode=edit"],
])("keeps %s on Pages", (_name, search) => {
  on()
  state.search = search
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByTestId("pages")).toBeTruthy()
})

it("keeps a restricted member on their own surface", () => {
  on()
  state.access = "restricted"
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByTestId("restricted")).toBeTruthy()
})

it("stays on Pages with the switch off", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
  render(<PageDetailClient />)
  expect(state.replace).not.toHaveBeenCalled()
  expect(screen.getByTestId("pages").dataset.slug).toBe("weekly-report")
})
