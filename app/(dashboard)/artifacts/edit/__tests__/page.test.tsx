import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

const state = vi.hoisted(() => ({ search: "page_id=p1&artifact=1&workspace_id=ws1", access: "trusted" }))
vi.mock("next/navigation", () => ({ useSearchParams: () => new URLSearchParams(state.search) }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => state.access }))
vi.mock("@/components/features/pages/artifact-project", () => ({
  ArtifactProject: ({ slug, back }: { slug: string; back?: { href: string; label: string } }) => <a data-testid="project" data-slug={slug} href={back?.href}>{back?.label}</a>,
}))
vi.mock("../../../pages/craft/editor/page", () => ({ default: () => <p>Word editor</p> }))

import ArtifactEditPage from "../page"

afterEach(() => { cleanup(); state.search = "page_id=p1&artifact=1&workspace_id=ws1"; state.access = "trusted" })

it("opens a project's source by its ID inside Artifacts, with a way back to the artifact", () => {
  render(<ArtifactEditPage />)
  expect(screen.getByTestId("project").dataset.slug).toBe("@p1")
  expect(screen.getByRole("link", { name: "Back to artifact" })).toHaveAttribute("href", "/artifacts/p1?workspace_id=ws1")
})

it("opens a native Word's editor without artifact=1", () => {
  state.search = "page_id=p1&workspace_id=ws1"
  render(<ArtifactEditPage />)
  expect(screen.getByText("Word editor")).toBeTruthy()
})

it.each([["no page", "artifact=1&workspace_id=ws1"], ["two pages", "page_id=a&page_id=b&artifact=1"]])("refuses %s", (_name, search) => {
  state.search = search
  render(<ArtifactEditPage />)
  expect(screen.getByRole("alert")).toHaveTextContent("This artifact is not available.")
  expect(screen.queryByTestId("project")).toBeNull()
})

it("keeps a restricted member out of the source editor", () => {
  state.access = "restricted"
  render(<ArtifactEditPage />)
  expect(screen.queryByTestId("project")).toBeNull()
})
