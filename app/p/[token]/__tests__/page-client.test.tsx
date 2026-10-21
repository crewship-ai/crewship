import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

import { PublicPageClient } from "../page-client"

// The public artifact page carries only the public bar (Harbor 2, #3410): the
// mark and what this is — no account, workspace or notification controls.

vi.mock("@/lib/use-url-segment", () => ({ useUrlSegment: () => "synthetic-token" }))
vi.mock("@/components/features/pages/public/use-public-page", () => ({
  usePublicPage: () => ({ status: "loading", page: null, message: null, submitting: false, submit: vi.fn() }),
}))

afterEach(() => cleanup())

it("renders the public bar with the Shared artifact crumb and nothing to act on", () => {
  render(<PublicPageClient />)
  const bar = screen.getByRole("banner")
  expect(within(bar).getByRole("navigation", { name: "Breadcrumb" })).toHaveTextContent("Crewship/Shared artifact")
  expect(within(bar).queryAllByRole("button")).toHaveLength(0)
  expect(within(bar).queryAllByRole("link")).toHaveLength(0)
  // The token never appears in the chrome.
  expect(bar).not.toHaveTextContent("synthetic-token")
})
