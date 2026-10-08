import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

vi.mock("@/components/ui/page-save-bar", () => ({ usePageSaveGuard: () => (go: () => void) => go() }))

import { WorkspaceScopeSection, type ScopeWorkspace } from "../workspace-scope"

const WS: ScopeWorkspace[] = [
  { id: "a", name: "Dess", slug: "dess", crews: 4, logoUrl: "https://cdn.example.com/dess.png" },
  { id: "b", name: "Ivana Viewer's Workspace", slug: "ivana", crews: 0 },
  { id: "c", name: "Tom Member's Workspace", slug: "tom", crews: 0 },
]
const all = { all: true, ids: new Set(WS.map((w) => w.id)) }

afterEach(() => cleanup())

describe("WorkspaceScopeSection", () => {
  it("draws a workspace's logo when it has one, initials otherwise", () => {
    const { container } = render(<WorkspaceScopeSection workspaces={WS} scope={all} onChange={vi.fn()} />)
    expect(container.querySelector("img[src='https://cdn.example.com/dess.png']")).not.toBeNull()
  })

  // Every signup gets a workspace of its own; most hold no crew. Where the
  // list is about crews' data, those stay folded until asked for.
  it("folds workspaces without crews away when asked, and shows them on demand", () => {
    render(<WorkspaceScopeSection workspaces={WS} scope={all} onChange={vi.fn()} hideEmpty />)
    expect(screen.getByText("Dess")).toBeInTheDocument()
    expect(screen.queryByText("Ivana Viewer's Workspace")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /Show 2 without crews/ }))
    expect(screen.getByText("Ivana Viewer's Workspace")).toBeInTheDocument()
  })

  it("lists every workspace by default", () => {
    render(<WorkspaceScopeSection workspaces={WS} scope={all} onChange={vi.fn()} />)
    expect(screen.getByText("Tom Member's Workspace")).toBeInTheDocument()
  })
})
