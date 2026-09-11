import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import * as React from "react"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"

const push = vi.fn()
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages/operations-lab",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({}),
}))

// A section that holds unsaved work, and three that do not. What is under test
// is the way out of the editor, not what any section draws.
let raiseDirty: (dirty: boolean) => void = () => {}
vi.mock("@/components/features/pages/editor/section-content", () => ({
  EditorContentSection: ({ onDirtyChange }: { onDirtyChange: (d: boolean) => void }) => {
    raiseDirty = onDirtyChange
    return <div data-testid="section-content">Content</div>
  },
}))
vi.mock("@/components/features/pages/editor/section-data-actions", () => ({
  EditorDataActionsSection: () => <div data-testid="section-data" />,
}))
vi.mock("@/components/features/pages/editor/section-access", () => ({
  EditorAccessSection: () => <div data-testid="section-access" />,
}))
vi.mock("@/components/features/pages/editor/section-history", () => ({
  EditorHistorySection: () => <div data-testid="section-history" />,
}))

import { PageEditorShell } from "@/components/features/pages/editor/page-editor-shell"
import { useEditorRoute } from "@/components/features/pages/editor/use-editor-route"
import { derivePageCapabilities } from "@/components/features/pages/editor/use-page-capabilities"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * Counter-review R3. The editor guarded its own address, Back, reload and the
 * workspace switcher — and every other link in the application walked past it.
 * The global sidebar is plain `next/link`, so leaving for Routines is a
 * client-side navigation that is neither a reload nor a popstate: the editor
 * unmounted and the half-typed form went with it, with nothing asked.
 *
 * This mounts the real shell over the real route hook and the real guard
 * registry, so the whole path is exercised rather than each half of it.
 */

const PAGE = { slug: "operations-lab", name: "Operations Lab", panels: [], has_application: false } as unknown as WirePageDetail

function Harness() {
  const nav = useEditorRoute("operations-lab")
  React.useEffect(() => {
    if (nav.mode !== "edit") nav.openEditor()
  }, [nav])
  return (
    <>
      {/* Stands in for the global sidebar: an ordinary in-app link, outside
          the editor's own chrome and knowing nothing about it. */}
      <a href="/routines">Routines</a>
      <PageEditorShell
        workspaceId="ws-1"
        slug="operations-lab"
        page={PAGE}
        loading={false}
        capabilities={derivePageCapabilities(PAGE)}
        navigation={nav}
      />
    </>
  )
}

function clickRoutines() {
  act(() => {
    screen.getByText("Routines").dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 }))
  })
}

beforeEach(() => {
  push.mockReset()
  window.history.replaceState(null, "", "/pages/operations-lab")
})
afterEach(cleanup)

describe("leaving the editor by the global navigation", () => {
  it("asks before a link takes unsaved work with it, and stays when told to", () => {
    render(<Harness />)
    act(() => raiseDirty(true))

    clickRoutines()

    expect(screen.getByText("Leave without saving?")).toBeTruthy()
    expect(push).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Stay here" }))
    expect(screen.queryByText("Leave without saving?")).toBeNull()
    expect(push).not.toHaveBeenCalled()
    // Still in the editor, still holding the work.
    expect(screen.getByTestId("section-content")).toBeTruthy()
  })

  it("performs the navigation it refused once the person discards", () => {
    render(<Harness />)
    act(() => raiseDirty(true))

    clickRoutines()
    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }))

    expect(push).toHaveBeenCalledWith("/routines")
  })

  it("does not ask when nothing is unsaved", () => {
    render(<Harness />)
    clickRoutines()
    expect(screen.queryByText("Leave without saving?")).toBeNull()
  })

  it("stops asking after the work is saved", () => {
    render(<Harness />)
    act(() => raiseDirty(true))
    act(() => raiseDirty(false))
    clickRoutines()
    expect(screen.queryByText("Leave without saving?")).toBeNull()
  })
})
