import { describe, it, expect, vi, beforeEach } from "vitest"
import React from "react"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  usePathname: () => "/pages/operations-lab",
  useSearchParams: () => new URLSearchParams(),
  useParams: () => ({ slug: "operations-lab" }),
}))
vi.mock("@/components/features/pages/editor/section-content", () => ({
  EditorContentSection: () => <div data-testid="section-content" />,
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
import { EDITOR_SECTION_LABEL, EDITOR_SECTION_SUMMARY, EDITOR_SECTIONS } from "@/lib/pages/editor-contract"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * #2515: Edit hands the whole content column to the editor. What that has to
 * mean on screen, stated as tests rather than as a screenshot:
 *
 *  · the rail lists every section with one line under its name, and marks
 *    the open one — a row of tabs said where you were and nothing about the
 *    other three;
 *  · the header says how to get back and what viewers see meanwhile, because
 *    the first question after the screen changes under you is whether
 *    anything you touch here is already live.
 */

function page(extra: Partial<WirePageDetail>): WirePageDetail {
  return { slug: "operations-lab", name: "Operations Lab", panels: [], has_application: false, ...extra } as unknown as WirePageDetail
}

function Harness({ detail }: { detail: WirePageDetail }) {
  const nav = useEditorRoute("operations-lab")
  const opened = React.useRef(false)
  React.useEffect(() => {
    if (opened.current) return
    opened.current = true
    nav.openEditor()
  }, [nav])
  if (nav.mode !== "edit") return <p>viewing</p>
  return (
    <PageEditorShell
      workspaceId="ws-1"
      slug="operations-lab"
      page={detail}
      loading={false}
      capabilities={derivePageCapabilities(detail)}
      navigation={nav}
    />
  )
}

describe("the editor as a mode over the whole page", () => {
  beforeEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/pages/operations-lab")
  })

  it("lists every section in the rail with its one-line summary, and marks the open one", () => {
    render(<Harness detail={page({})} />)
    const rail = screen.getByRole("navigation", { name: "Editor sections" })
    for (const id of EDITOR_SECTIONS) {
      const button = within(rail).getByRole("button", { name: new RegExp(EDITOR_SECTION_LABEL[id]) })
      expect(button.textContent).toContain(EDITOR_SECTION_SUMMARY[id])
    }
    expect(within(rail).getByRole("button", { name: /Content/ }).getAttribute("aria-current")).toBe("page")
    expect(within(rail).getByRole("button", { name: /Access/ }).getAttribute("aria-current")).toBeNull()
  })

  it("opens the section the rail names and moves the mark with it", () => {
    render(<Harness detail={page({})} />)
    const rail = screen.getByRole("navigation", { name: "Editor sections" })
    fireEvent.click(within(rail).getByRole("button", { name: /Access/ }))
    expect(screen.getByTestId("section-access")).toBeTruthy()
    expect(screen.queryByTestId("section-content")).toBeNull()
    expect(within(rail).getByRole("button", { name: /Access/ }).getAttribute("aria-current")).toBe("page")
    expect(screen.getByText(/Editing Access/)).toBeTruthy()
  })

  it("says that viewers keep the live publication while an application Page is edited", () => {
    render(<Harness detail={page({ has_application: true, publication_version: 4 } as Partial<WirePageDetail>)} />)
    expect(screen.getByText(/Viewers still see publication 4/)).toBeTruthy()
  })

  it("says the draft is unpublished when the application has never shipped", () => {
    render(<Harness detail={page({ has_project: true } as Partial<WirePageDetail>)} />)
    expect(screen.getByText(/The application draft is not published/)).toBeTruthy()
  })

  it("offers one way back, named for where it goes", () => {
    render(<Harness detail={page({})} />)
    fireEvent.click(screen.getByRole("button", { name: /back to page/i }))
    expect(screen.getByText("viewing")).toBeTruthy()
  })
})
