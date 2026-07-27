import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import { SaveFooter } from "../save-footer"

describe("SaveFooter", () => {
  beforeEach(() => cleanup())

  it("renders nothing while the form is clean", () => {
    const { container } = render(
      <SaveFooter dirty={false} status="idle" onSave={vi.fn()} onCancel={vi.fn()} />,
    )
    // A permanently visible Save bar is noise — it only earns its space once
    // there is something to save.
    expect(container).toBeEmptyDOMElement()
  })

  it("appears with Save and Cancel once dirty", () => {
    render(<SaveFooter dirty status="idle" onSave={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole("button", { name: /save/i })).toBeEnabled()
    expect(screen.getByRole("button", { name: /cancel/i })).toBeInTheDocument()
  })

  it("fires the handlers", () => {
    const onSave = vi.fn()
    const onCancel = vi.fn()
    render(<SaveFooter dirty status="idle" onSave={onSave} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole("button", { name: /^save/i }))
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }))
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it("locks both buttons while saving", () => {
    render(<SaveFooter dirty status="saving" onSave={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole("button", { name: /saving/i })).toBeDisabled()
    // Cancelling mid-write would leave the user unsure which state won.
    expect(screen.getByRole("button", { name: /cancel/i })).toBeDisabled()
  })

  it("stays visible on the saved confirmation even though the form went clean", () => {
    render(<SaveFooter dirty={false} status="saved" onSave={vi.fn()} onCancel={vi.fn()} />)
    // submit() rebases the baseline, so dirty flips false the instant the write
    // lands. Collapsing right then would swallow the only success signal.
    expect(screen.getByText(/saved/i)).toBeInTheDocument()
  })

  it("shows the server's reason for a failed save", () => {
    render(
      <SaveFooter
        dirty
        status="error"
        error="workspace name already taken"
        onSave={vi.fn()}
        onCancel={vi.fn()}
      />,
    )
    expect(screen.getByText(/workspace name already taken/i)).toBeInTheDocument()
    // Still retryable — the draft is intact.
    expect(screen.getByRole("button", { name: /^save/i })).toBeEnabled()
  })

  it("can require a reason before Save unlocks", () => {
    const { rerender } = render(
      <SaveFooter dirty status="idle" reason="" onReasonChange={vi.fn()} onSave={vi.fn()} onCancel={vi.fn()} />,
    )
    // Policy and agent-autonomy writes carry a mandatory audit note; the
    // footer has to be able to gate on it or those cards can't adopt it.
    expect(screen.getByRole("button", { name: /^save/i })).toBeDisabled()

    rerender(
      <SaveFooter dirty status="idle" reason="granting for pilot" onReasonChange={vi.fn()} onSave={vi.fn()} onCancel={vi.fn()} />,
    )
    expect(screen.getByRole("button", { name: /^save/i })).toBeEnabled()
  })

  it("has no reason field unless one is asked for", () => {
    render(<SaveFooter dirty status="idle" onSave={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.queryByRole("textbox")).toBeNull()
  })

  it("lets the caller veto Save on its own validation", () => {
    render(<SaveFooter dirty status="idle" canSave={false} onSave={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole("button", { name: /^save/i })).toBeDisabled()
  })

  it("docks to the viewport bottom on small screens only", () => {
    const { container } = render(<SaveFooter dirty status="idle" onSave={vi.fn()} onCancel={vi.fn()} />)
    const root = container.firstElementChild as HTMLElement
    // Same component, different anchoring — no second mobile implementation.
    expect(root.className).toContain("max-sm:fixed")
    expect(root.className).toContain("max-sm:bottom-0")
  })

  it("announces itself to assistive tech when it appears", () => {
    render(<SaveFooter dirty status="idle" onSave={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole("status")).toBeInTheDocument()
  })
})
