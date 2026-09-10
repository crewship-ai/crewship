import React from "react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"

import type { PagePreview } from "@/hooks/use-page-preview"

/**
 * The preview's security coverage, re-homed.
 *
 * These six properties were asserted against `PagePreviewDialog`, which had
 * one entry point and no longer has it. They are not properties of that
 * dialog — they are properties of *any* surface in this product that mounts
 * `PagePreviewFrame`, and `ReviewPreview` is now the only one. If the dialog's
 * test file is deleted without these, the whole set stops being enforced
 * anywhere while every remaining suite still passes.
 *
 * `PagePreviewFrame` is deliberately NOT mocked here. Three of the six
 * (`about:srcdoc`, the same-origin rule, the unsupported browser) are enforced
 * inside the frame by `validatePreview` and `supportsPageApplications`; a
 * mocked frame would turn all three into tests of the mock. The sibling suite
 * `application-review.test.tsx` mocks the frame on purpose, because what it
 * tests is the review screen's own judgement.
 */

const state = vi.hoisted(() => ({
  query: { data: null as PagePreview | null, isError: false, error: null as Error | null, refetch: vi.fn() },
  build: { isPending: false, error: null as Error | null, mutate: vi.fn() },
}))
vi.mock("@/hooks/use-page-preview", () => ({ usePagePreview: () => state }))

import { ReviewPreview } from "@/components/features/pages/editor/review-preview"

const FRAME_TITLE = "Candidate application preview"

beforeEach(() => {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 Chrome/140.0.0.0 Safari/537.36")
  const w = window as unknown as { happyDOM?: { settings: { disableIframePageLoading: boolean } } }
  if (w.happyDOM) w.happyDOM.settings.disableIframePageLoading = true
  state.query.data = {
    revision: 7,
    runtime_url: "https://pages.example.net/api/v1/pages/runtime/bootstrap",
    build: { id: "build-7", source_revision: 7, state: "ready" },
    artifact: { format: "crewship-page-preview/v1", javascript: "void 0", css: "", toolchain: "test" },
  }
  state.query.error = null
  state.query.isError = false
  state.build.error = null
  state.build.isPending = false
})
afterEach(cleanup)

const props = {
  workspaceId: "ws",
  slug: "operations-lab",
  page: { slug: "operations-lab", name: "Operations Lab", panels: [] } as never,
  candidateRevision: 7,
  onReturn: vi.fn(),
}

it("removes cached executable content when preview authorization fails", () => {
  const { rerender } = render(<ReviewPreview {...props} />)
  expect(screen.getByTitle(FRAME_TITLE)).toBeTruthy()
  // react-query keeps the last successful `data` through a failed refetch, so
  // the artifact is still in hand. Unmounting on `isError` — rather than
  // rendering an error beside a live frame — is what stops the code running.
  state.query.error = new Error("You need permission to edit this Page to open its application preview.")
  state.query.isError = true
  rerender(<ReviewPreview {...props} />)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()
  expect(screen.getByRole("alert").textContent).toMatch(/permission/)
})

it("stops executing content when the Page data disappears", () => {
  const { rerender } = render(<ReviewPreview {...props} />)
  expect(screen.getByTitle(FRAME_TITLE)).toBeTruthy()
  rerender(<ReviewPreview {...props} page={null} />)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()
  expect(screen.getByText(/This Page's data is not available/)).toBeTruthy()
})

it("stops the preview from a host control outside the frame and says it stopped", () => {
  const { rerender } = render(<ReviewPreview {...props} />)
  const stop = screen.getByRole("button", { name: "Stop preview" })
  // The control must live outside the iframe: there is no in-frame stop
  // message, so a stop the host cannot perform is not a stop at all.
  expect(stop.closest("iframe")).toBeNull()
  fireEvent.click(stop)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()
  expect(screen.getByText("Preview stopped.")).toBeTruthy()
  // And it stays stopped across an unrelated re-render.
  rerender(<ReviewPreview {...props} />)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()
})

it("refuses an invalid runtime URL instead of executing it", () => {
  state.query.data!.runtime_url = "about:srcdoc"
  render(<ReviewPreview {...props} />)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()
  expect(screen.getByRole("alert").textContent).toMatch(/separate application domain/)
})

it("requires the server development flag for a same-origin runtime and keeps the opaque sandbox", () => {
  state.query.data!.runtime_url = window.location.origin + "/api/v1/pages/runtime/bootstrap"
  const { rerender } = render(<ReviewPreview {...props} />)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()

  state.query.data!.development_same_origin = true
  rerender(<ReviewPreview {...props} />)
  expect(screen.getByTitle(FRAME_TITLE).getAttribute("sandbox")).toBe("allow-scripts")

  // The flag is the server's to give: the client may not decide it locally.
  state.query.data!.development_same_origin = false
  rerender(<ReviewPreview {...props} />)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()
})

it("does not execute an application in an unsupported browser", () => {
  vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 Firefox/140.0")
  render(<ReviewPreview {...props} />)
  expect(screen.queryByTitle(FRAME_TITLE)).toBeNull()
  expect(screen.getByRole("alert").textContent).toContain("desktop Chrome or Edge")
})

it("never hands the frame an action handler, in any of these states", () => {
  render(<ReviewPreview {...props} />)
  // A draft preview must not execute the application's actions. The runtime
  // refuses an unhandled request, and omitting the handler is what makes that
  // refusal the only possible outcome rather than a policy someone can flip.
  expect(screen.getByText(/does not run the application's actions/)).toBeTruthy()
  expect(screen.getByTitle(FRAME_TITLE).getAttribute("sandbox")).toBe("allow-scripts")
})
