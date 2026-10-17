import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"

import { WELCOME_FLAG, WelcomeChecklist } from "../welcome-checklist"

// The global setup stubs localStorage with no memory; these tests are about
// what persists, so give it one.
const store = new Map<string, string>()
beforeEach(() => {
  store.clear()
  vi.mocked(window.localStorage.getItem).mockImplementation((k: string) => store.get(k) ?? null)
  vi.mocked(window.localStorage.setItem).mockImplementation((k: string, v: string) => { store.set(k, v) })
  vi.mocked(window.localStorage.removeItem).mockImplementation((k: string) => { store.delete(k) })
})
afterEach(() => { cleanup(); vi.mocked(window.localStorage.getItem).mockImplementation(() => null) })

describe("Get started card", () => {
  it("renders only with justOnboarded flag", async () => {
    const view = render(<WelcomeChecklist />)
    await act(async () => {})
    expect(screen.queryByRole("region", { name: "Get started" })).toBeNull()
    view.unmount()
    window.localStorage.setItem(WELCOME_FLAG, "1")
    render(<WelcomeChecklist />)
    await act(async () => {})
    const card = screen.getByRole("region", { name: "Get started" })
    expect(card.querySelectorAll('[data-slot="dash-line"]')).toHaveLength(3)
    expect(screen.getByRole("link", { name: "Browse agents" })).toHaveAttribute("href", "/crews")
  })

  it("Dismiss persists and hides", async () => {
    window.localStorage.setItem(WELCOME_FLAG, "1")
    const view = render(<WelcomeChecklist />)
    await act(async () => {})
    fireEvent.click(screen.getByRole("button", { name: "Dismiss welcome checklist" }))
    expect(screen.queryByRole("region", { name: "Get started" })).toBeNull()
    expect(window.localStorage.getItem(WELCOME_FLAG)).toBeNull()
    view.unmount()
    window.localStorage.setItem(WELCOME_FLAG, "1")
    render(<WelcomeChecklist />)
    await act(async () => {})
    expect(screen.queryByRole("region", { name: "Get started" })).toBeNull()
  })

  it("opens the first agent's chat when onboarding left its slug", async () => {
    window.localStorage.setItem(WELCOME_FLAG, "1")
    window.localStorage.setItem("crewship.firstAgentSlug", "scout")
    render(<WelcomeChecklist firstAgentId="a1" />)
    await act(async () => {})
    expect(screen.getByRole("link", { name: "Open chat" })).toHaveAttribute("href", "/chat/scout")
  })
})
