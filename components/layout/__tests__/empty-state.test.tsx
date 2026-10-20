import { describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { Webhook } from "lucide-react"

import { EmptyState } from "../empty-state"

// RT-10 / COM: the one EmptyState has a compact size (flush with the surface,
// for a tab or a card body) and an `action` slot for its one button.
describe("EmptyState", () => {
  it("compact is flush: no Card around it, marked data-size=compact", () => {
    render(<EmptyState size="compact" icon={Webhook} title="No webhooks yet" description="Create one." />)
    const root = screen.getByRole("heading", { name: "No webhooks yet" }).closest("[data-slot=empty-state]")!
    expect(root).toHaveAttribute("data-size", "compact")
    expect(root.closest("[data-slot=card]")).toBeNull()
  })

  it("the card size keeps the Card", () => {
    render(<EmptyState icon={Webhook} title="Nothing" description="Here." />)
    const root = screen.getByRole("heading", { name: "Nothing" }).closest("[data-slot=empty-state]")!
    expect(root).toHaveAttribute("data-size", "card")
    expect(root.closest("[data-slot=card]")).not.toBeNull()
  })

  it("renders the action under the description and it works", () => {
    const onClick = vi.fn()
    render(<EmptyState size="compact" icon={Webhook} title="T" description="D" action={<button type="button" onClick={onClick}>Add webhook</button>} />)
    const btn = screen.getByRole("button", { name: "Add webhook" })
    expect(btn.closest("[data-slot=empty-state-action]")).not.toBeNull()
    const text = screen.getByText("D")
    expect(text.compareDocumentPosition(btn) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    fireEvent.click(btn)
    expect(onClick).toHaveBeenCalledOnce()
  })

  it("keeps the old inline size working as the same flush layout", () => {
    render(<EmptyState size="inline" icon={Webhook} title="Old" description="Caller." />)
    expect(screen.getByRole("heading", { name: "Old" }).closest("[data-slot=empty-state]")).toHaveAttribute("data-size", "compact")
  })
})
