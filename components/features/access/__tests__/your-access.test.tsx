import { render, screen } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { YourAccess } from "../your-access"

const state = vi.hoisted(() => ({
  value: null as null | { access: { actions: Record<string, { state: "allowed" | "conditional" | "denied"; reason: string }> } | null; loading: boolean; error: boolean },
}))
vi.mock("@/hooks/use-access-me", () => ({ useAccessMe: () => state.value }))

const actions = [{ key: "run", label: "Run" }]
beforeEach(() => { state.value = { access: null, loading: false, error: false } })

describe("YourAccess", () => {
  it("does not imply permission before an answer or after an error", () => {
    const view = render(<YourAccess url="/access/me" actions={actions} />)
    expect(screen.getByRole("status")).toHaveTextContent("Checking your access")
    state.value = { access: null, loading: false, error: true }
    view.rerender(<YourAccess url="/access/me" actions={actions} />)
    expect(screen.getByRole("alert")).toHaveTextContent("could not be determined")
    expect(screen.queryByText(/Allowed —/)).not.toBeInTheDocument()
  })

  it("names conditional access and keeps unknown actions unknown", () => {
    state.value = { access: { actions: { run: { state: "conditional", reason: "runtime_preflight_required" } } }, loading: false, error: false }
    render(<YourAccess url="/access/me" actions={[...actions, { key: "edit", label: "Edit" }]} />)
    expect(screen.getByText(/Conditional — Eligible; checked again when the run starts/)).toBeInTheDocument()
    expect(screen.getByText(/Could not be determined/)).toBeInTheDocument()
  })
})
