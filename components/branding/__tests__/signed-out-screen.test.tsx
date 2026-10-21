import { describe, it, expect, vi, afterEach } from "vitest"
import { readFileSync } from "node:fs"
import path from "node:path"
import { act, cleanup, render, screen } from "@testing-library/react"

import { RestartingScreen, SignedOutScreen } from "../signed-out-screen"

// #3787: the four full-screen states share one anatomy — the sail in its blue
// tile, a title, one sentence, one action — on the app ground.

const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
afterEach(() => { cleanup(); vi.useRealTimers() })

describe("signed-out screens", () => {
  it.each([
    ["expired", "Your session expired", "Sign in"],
    ["revoked", "Signed out on this device", "Sign in"],
    ["signed-out", "You're signed out", "Sign in again"],
  ] as const)("%s: the tile, the title and one primary", (kind, title, action) => {
    render(<SignedOutScreen kind={kind} onSignIn={() => {}} />)
    const root = screen.getByRole("heading", { level: 1, name: title }).closest("[data-slot=signed-out-screen]")!
    expect(root.tagName).toBe("MAIN")
    expect(root.querySelector("[data-slot=signed-out-mark] svg")).not.toBeNull()
    expect(screen.getAllByRole("button").filter((b) => b.textContent === action)).toHaveLength(1)
  })

  it("expired without a known email offers only Use another account", () => {
    render(<SignedOutScreen kind="expired" onSignIn={() => {}} onUseAnotherAccount={() => {}} />)
    expect(screen.queryByText(/Signed in as/)).toBeNull()
    expect(screen.getByRole("button", { name: "Use another account" })).toBeInTheDocument()
  })

  it("restarting counts down to the next try and never shows a negative second", () => {
    vi.useFakeTimers({ now: 0 })
    render(<RestartingScreen retryAt={3_000} onRetry={() => {}} />)
    expect(screen.getByText(/Next try in 3 s/)).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(5_000) })
    expect(screen.getByText(/Trying now…/)).toBeInTheDocument()
  })

  it("the sail pulses only when the person allows motion", () => {
    const rule = /@media \(prefers-reduced-motion: no-preference\)\s*\{[^}]*\.signed-out-pulse\s*\{[^}]*animation:/
    expect(css).toMatch(rule)
    // …and nowhere outside that query.
    expect(css.replace(rule, "")).not.toMatch(/\.signed-out-pulse\s*\{[^}]*animation:/)
  })
})
