import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({ params: new URLSearchParams("workspace_id=ws1"), replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({ useSearchParams: () => nav.params, useRouter: () => ({ replace: nav.replace, push: nav.push }) }))
vi.mock("@/hooks/use-page-workspace", () => ({ usePageWorkspace: () => ({ workspaceId: "ws1", loading: false, linkUnavailable: false }) }))
vi.mock("@/components/features/artifacts-library/artifacts-library", () => ({
  ArtifactsLibrary: ({ workspaceId, item, onSelect }: { workspaceId: string; item: string | null; onSelect: (slug: string | null) => void }) =>
    <div data-testid="library" data-ws={workspaceId} data-item={item ?? ""}><button onClick={() => onSelect("site")}>pick</button><button onClick={() => onSelect(null)}>back</button></div>,
}))

import ArtifactsPage from "../page"

beforeEach(() => { nav.params = new URLSearchParams("workspace_id=ws1"); nav.replace.mockReset(); nav.push.mockReset(); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true") })
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("with the flag off sends saved links to Pages", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
  render(<ArtifactsPage />)
  expect(screen.queryByTestId("library")).toBeNull()
  expect(nav.replace).toHaveBeenCalledWith("/pages?workspace_id=ws1")
})

it("keeps the open artifact in the URL next to the workspace", () => {
  nav.params = new URLSearchParams("workspace_id=ws1&item=brief")
  render(<ArtifactsPage />)
  expect(screen.getByTestId("library").dataset).toMatchObject({ ws: "ws1", item: "brief" })
  fireEvent.click(screen.getByText("pick"))
  expect(nav.push).toHaveBeenCalledWith("/artifacts?workspace_id=ws1&item=site")
  fireEvent.click(screen.getByText("back"))
  expect(nav.push).toHaveBeenLastCalledWith("/artifacts?workspace_id=ws1")
})
