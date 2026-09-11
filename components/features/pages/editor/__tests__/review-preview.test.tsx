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
  frameProps: [] as Array<Record<string, unknown>>,
}))
vi.mock("@/hooks/use-page-preview", () => ({ usePagePreview: () => state }))

// A recording pass-through, NOT a stand-in: the real `PagePreviewFrame` still
// renders, so `validatePreview` and `supportsPageApplications` are the ones
// under test above, while the props it was handed stay inspectable. Asserting
// a copy string and a sandbox value cannot detect an added `onRequest`; only
// looking at the props can.
vi.mock("@/components/features/pages/page-preview", async importActual => {
  const actual = await importActual<typeof import("@/components/features/pages/page-preview")>()
  return {
    ...actual,
    PagePreviewFrame: (props: Record<string, unknown>) => {
      state.frameProps.push(props)
      return React.createElement(actual.PagePreviewFrame, props as never)
    },
  }
})

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
  state.frameProps = []
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

it("never hands the frame an action handler", () => {
  render(<ReviewPreview {...props} />)
  // A draft preview must not execute the application's actions. The runtime
  // refuses an unhandled request, and omitting the handler is what makes that
  // refusal the only possible outcome rather than a policy someone can flip.
  // Asserted structurally: adding an `onRequest` must turn this red.
  expect(state.frameProps).toHaveLength(1)
  expect("onRequest" in state.frameProps[0]).toBe(false)
  expect(state.frameProps[0].onRequest).toBeUndefined()
  expect(screen.getByText(/does not run the application's actions/)).toBeTruthy()
  expect(screen.getByTitle(FRAME_TITLE).getAttribute("sandbox")).toBe("allow-scripts")
})

it("labels a stale build by the revision that is actually running, not the one under review", () => {
  state.query.data!.build = { id: "build-5", source_revision: 5, state: "ready" }
  render(<ReviewPreview {...props} />)
  // The frame IS mounted — an older artifact is still worth looking at — so
  // the header must not call it the candidate.
  expect(screen.getByTitle(FRAME_TITLE)).toBeTruthy()
  expect(screen.getByText(/Showing draft 5 — not the draft 7 under review/)).toBeTruthy()
  expect(screen.queryByText(/^Draft 7 ·/)).toBeNull()
})

it("can still start a build when the preview read failed but the revision under review is known", () => {
  // `retry: false`, so one blip is enough to set this. Disabling Build here
  // wedges the only control that clears the state.
  state.query.data = null
  state.query.isError = true
  state.query.error = new Error("Could not load the application preview.")
  render(<ReviewPreview {...props} />)

  const build = screen.getByRole("button", { name: "Build preview" }) as HTMLButtonElement
  expect(build.disabled).toBe(false)
  fireEvent.click(build)
  // The revision comes from the review snapshot, not from the failed read.
  expect(state.build.mutate).toHaveBeenCalledWith(7)
  // And the failure is still accounted for on screen, not swallowed.
  expect(screen.getByRole("alert").textContent).toMatch(/Could not load the application preview/)
})

it("leaves Build dead only when no revision is known from either source", () => {
  state.query.data = null
  state.query.isError = true
  state.query.error = new Error("This Page has no application draft yet.")
  render(<ReviewPreview {...props} candidateRevision={null} />)
  expect((screen.getByRole("button", { name: "Build preview" }) as HTMLButtonElement).disabled).toBe(true)
})

it("shows the keyboard user where focus went when this pane takes it", () => {
  // Entering the preview parks focus on this heading. A browser pass found
  // the move invisible: `outline-none` with nothing in its place, and
  // `focus-visible:` would not have fired for a programmatic focus on a
  // tabIndex={-1} element. happy-dom computes no Tailwind, so what is held
  // here is the rule that gets emitted, next to the move it accompanies.
  render(<ReviewPreview {...props} />)
  const heading = screen.getByRole("heading", { name: "Candidate preview" })
  expect(document.activeElement).toBe(heading)
  expect(heading.getAttribute("tabindex")).toBe("-1")
  expect(heading.className).toMatch(/focus:ring-2/)
  expect(heading.className).not.toMatch(/focus-visible:ring/)
})
