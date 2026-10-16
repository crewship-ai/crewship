import { render, screen, cleanup } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { ArtifactSourceDiff } from "../artifact-source-diff"
const state = vi.hoisted(() => ({ left: "<p>before</p>", right: "<p>after</p>", error: false, wrongRevision: false }))
vi.mock("@/hooks/use-page-artifact", () => ({ usePageArtifact: (_workspace: string, _slug: string, revision: number) => ({ query: { error: state.error ? new Error("denied") : null, data: { revision: state.wrongRevision ? 99 : revision, html: revision === 1 ? state.left : state.right } } }) }))
afterEach(cleanup)
beforeEach(() => { state.left = "<p>before</p>"; state.right = "<p>after</p>"; state.error = false; state.wrongRevision = false })
const show = () => render(<ArtifactSourceDiff workspaceId="w" slug="report" before={1} after={2} />)
describe("Artifact source comparison", () => {
  it("renders hostile source as escaped diff text", () => {
    state.right = '<script>alert(1)</script>'
    const { container } = show()
    expect(screen.getByLabelText("HTML source changes").textContent).toContain('+<script>alert(1)</script>')
    expect(container.querySelector("script")).toBeNull()
    expect(screen.getByLabelText("HTML source changes").textContent).toContain('-<p>before</p>')
  })
  it("does not compare placeholder revisions or access-denied cached source", () => {
    state.wrongRevision = true
    const view = show()
    expect(screen.getByRole("status")).toBeTruthy()
    state.wrongRevision = false; state.error = true
    view.rerender(<ArtifactSourceDiff workspaceId="w" slug="report" before={1} after={2} />)
    expect(screen.getByRole("alert")).toBeTruthy()
    expect(screen.queryByLabelText("HTML source changes")).toBeNull()
  })
  it("bounds large single-line documents without pretending they are identical", () => {
    state.left = "x".repeat(200001)
    show()
    expect(screen.getByText(/exceeds the 200,000-character/)).toBeTruthy()
    expect(screen.queryByLabelText("HTML source changes")).toBeNull()
  })
})
