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
import { EDITOR_SECTION_LABEL, EDITOR_SECTIONS } from "@/lib/pages/editor-contract"
import type { WirePageDetail } from "@/hooks/use-page-grants"

/**
 * #2515: Edit hands the whole content column to the editor. What that has to
 * mean on screen, stated as tests rather than as a screenshot:
 *
 *  · every section is on the page at once, as a landmark named for it, laid
 *    out the way an issue is — the rail of four that showed one at a time
 *    read as a settings wizard beside the rest of the product;
 *  · the address still names a section, and naming one hands focus to it
 *    without hiding the other three;
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
    <>
      {/* Stands in for the in-editor links that move between sections —
          "Manage producer access", "Data & actions" — which the mocked
          sections do not draw. */}
      <button type="button" onClick={() => nav.setSection("access")}>
        go to access
      </button>
      <PageEditorShell
        workspaceId="ws-1"
        slug="operations-lab"
        page={detail}
        loading={false}
        capabilities={derivePageCapabilities(detail)}
        navigation={nav}
      />
    </>
  )
}

describe("the editor as a mode over the whole page", () => {
  beforeEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/pages/operations-lab")
  })

  it("lays every section out at once, each a landmark named for it, under the Page's own header", () => {
    render(<Harness detail={page({})} />)
    for (const id of EDITOR_SECTIONS) {
      const frame = screen.getByRole("region", { name: EDITOR_SECTION_LABEL[id] })
      expect(within(frame).getByTestId(`section-${id}`)).toBeTruthy()
    }
    // No second rail: the Pages list on the left is the one rail every
    // surface shares, and the sections are cards, not screens.
    expect(screen.queryByRole("navigation", { name: "Editor sections" })).toBeNull()
    // The header is the issue header: the name first, the state as pills,
    // the way back top-right — and the Properties card beside the sections.
    const header = screen.getByTestId("page-editor-header")
    expect(within(header).getByRole("heading", { name: "Operations Lab" })).toBeTruthy()
    expect(header.textContent).toContain("0 panels")
    expect(screen.getByTestId("page-properties")).toBeTruthy()
  })

  it("hands focus to the section the address names, and keeps the other three on screen", () => {
    render(<Harness detail={page({})} />)
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Operations Lab" }))
    fireEvent.click(screen.getByRole("button", { name: "go to access" }))
    expect(window.location.search).toContain("section=access")
    expect(document.activeElement).toBe(screen.getByRole("region", { name: "Access" }))
    expect(screen.getByTestId("section-content")).toBeTruthy()
    expect(screen.getByTestId("section-access")).toBeTruthy()
  })

  it("names the application as a pill, with its publication", () => {
    render(<Harness detail={page({ has_application: true, has_project: true, publication_version: 4 } as Partial<WirePageDetail>)} />)
    const header = screen.getByTestId("page-editor-header")
    expect(header.textContent).toContain("Custom application")
    expect(header.textContent).toContain("Publication 4")
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
