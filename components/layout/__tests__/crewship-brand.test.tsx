import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { readFileSync } from "node:fs"
import path from "node:path"

// The owner, 19 Nov (#5552): the "Crewship X is available" banner under the
// top bar of every page is gone. The Crewship sail moved from the foot of the
// rail into the top bar, after Activity and Inbox, still in the brand's lit
// blue tile. A newer release puts a dot on it; the panel names the version,
// links the release notes and can mark it as seen, per version.

const access = vi.hoisted(() => ({ mode: "trusted" as "trusted" | "restricted" }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => access.mode }))
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

import { CrewshipMenu, SEEN_KEY_PREFIX } from "@/components/layout/crewship-brand"
import { DOCS_URL, RELEASES_URL, SUPPORT_URL } from "@/lib/crewship-links"

let version: Record<string, unknown>
const flush = async () => { await act(async () => { await Promise.resolve(); await Promise.resolve(); await Promise.resolve() }) }

beforeEach(() => {
  access.mode = "trusted"
  version = { current: "0.8.0", latest: "0.9.0", newer: true, url: "https://example.test/notes" }
  apiFetch.mockImplementation((url: string) =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve(url.endsWith("/version") ? version : { edition: "community" }),
    }),
  )
  // vitest.setup.ts stubs localStorage with no-op mocks; give it a memory.
  const store = new Map<string, string>()
  vi.mocked(localStorage.getItem).mockImplementation((key: string) => store.get(key) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((key: string, value: string) => { store.set(key, String(value)) })
})
afterEach(() => {
  cleanup()
  apiFetch.mockReset()
})

describe("the Crewship sail in the top bar", () => {
  it("wears the lit tile, with a dot while a newer version is unseen", async () => {
    render(<CrewshipMenu />)
    await flush()
    const trigger = screen.getByRole("button", { name: "Crewship: version 0.9.0 is available" })
    expect(trigger.querySelector(".bar-brand svg")).not.toBeNull()
    expect(screen.getByTestId("crewship-dot")).toBeInTheDocument()
  })

  it("has no dot when the build is current", async () => {
    version = { current: "0.9.0", latest: "0.9.0", newer: false, url: null }
    render(<CrewshipMenu />)
    await flush()
    expect(screen.getByRole("button", { name: "Crewship" })).toBeInTheDocument()
    expect(screen.queryByTestId("crewship-dot")).toBeNull()
  })

  it("names the new version and links the release notes", async () => {
    render(<CrewshipMenu />)
    await flush()
    fireEvent.click(screen.getByTestId("crewship-trigger"))
    await flush()
    const update = screen.getByTestId("crewship-update")
    expect(update).toHaveTextContent("New version available")
    expect(update).toHaveTextContent("0.9.0 is out. You have 0.8.0.")
    expect(screen.getByRole("link", { name: "Release notes" })).toHaveAttribute("href", "https://example.test/notes")
    expect(screen.getByTestId("crewship-build")).toHaveTextContent("Community Edition · 0.8.0")
  })

  it("Mark as seen hides the dot for that version only", async () => {
    const { unmount } = render(<CrewshipMenu />)
    await flush()
    fireEvent.click(screen.getByTestId("crewship-trigger"))
    fireEvent.click(screen.getByRole("button", { name: "Mark as seen" }))
    expect(screen.queryByTestId("crewship-dot")).toBeNull()
    expect(localStorage.getItem(SEEN_KEY_PREFIX + "0.9.0")).toBe("1")
    // The panel still offers the notes once seen.
    expect(screen.getByRole("link", { name: "Release notes" })).toBeInTheDocument()
    // The button left with the dot; focus stays in the panel.
    await act(async () => { await new Promise((r) => requestAnimationFrame(() => r(null))) })
    expect(screen.getByRole("link", { name: "Release notes" })).toHaveFocus()
    unmount()

    render(<CrewshipMenu />)
    await flush()
    expect(screen.queryByTestId("crewship-dot")).toBeNull()
    cleanup()

    version = { current: "0.8.0", latest: "0.10.0", newer: true, url: null }
    render(<CrewshipMenu />)
    await flush()
    expect(screen.getByTestId("crewship-dot")).toBeInTheDocument()
  })

  it("a version dismissed on the old banner stays seen", async () => {
    localStorage.setItem("crewship.update-banner.dismissed:0.9.0", "1")
    render(<CrewshipMenu />)
    await flush()
    expect(screen.queryByTestId("crewship-dot")).toBeNull()
  })

  it("a restricted session gets the sail and About, without a version check", async () => {
    access.mode = "restricted"
    render(<CrewshipMenu />)
    await flush()
    expect(screen.getByRole("button", { name: "Crewship" })).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("crewship-trigger"))
    fireEvent.click(screen.getByRole("button", { name: "About Crewship" }))
    await screen.findByRole("dialog", { name: "About Crewship" })
    await flush()
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it("links the docs, what's new and support, and opens About Crewship", async () => {
    render(<CrewshipMenu />)
    await flush()
    fireEvent.click(screen.getByTestId("crewship-trigger"))
    expect(screen.getByRole("link", { name: /Documentation/ })).toHaveAttribute("href", DOCS_URL)
    expect(screen.getByRole("link", { name: /What's new/ })).toHaveAttribute("href", RELEASES_URL)
    expect(screen.getByRole("link", { name: /Report a problem/ })).toHaveAttribute("href", SUPPORT_URL)
    fireEvent.click(screen.getByRole("button", { name: "About Crewship" }))
    const dialog = await screen.findByRole("dialog", { name: "About Crewship" })
    expect(dialog).toHaveTextContent(/AI agents/)
  })
})

describe("where the sail lives", () => {
  it("is in the top bar after the bells, and no longer in the rail", () => {
    const toolbar = readFileSync(path.resolve(__dirname, "../app-toolbar.tsx"), "utf8")
    expect(toolbar.indexOf("<CrewshipMenu")).toBeGreaterThan(toolbar.indexOf("<InboxBell"))
    const sidebar = readFileSync(path.resolve(__dirname, "../app-sidebar.tsx"), "utf8")
    expect(sidebar).not.toMatch(/<Crewship(Brand|Menu)/)
  })

  it("no page carries an update banner any more", () => {
    const layout = readFileSync(path.resolve(__dirname, "../../../app/(dashboard)/layout.tsx"), "utf8")
    expect(layout).not.toMatch(/UpdateBanner/)
  })

  it("wears the brand's own lit blue, whatever the style", () => {
    const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
    const rule = css.slice(css.indexOf(".bar-brand {"), css.indexOf("}", css.indexOf(".bar-brand {")))
    expect(rule).toMatch(/#1B75FE/i)
    expect(rule).toMatch(/box-shadow:.*rgb\(27 117 254/)
  })
})

// Round 63 (#3784): the rail's identities — workspace and you — are one
// 28 px size on the same axis, folded or open. The sail was the third until
// it moved to the top bar.
describe("identity sizes", () => {
  it("the workspace mark and the avatar are 28 px", () => {
    const ws = readFileSync(path.resolve(__dirname, "../workspace-switcher.tsx"), "utf8")
    const me = readFileSync(path.resolve(__dirname, "../user-menu.tsx"), "utf8")
    expect(me).toMatch(/UserAvatar[^\n]*className="size-\[28px\]"/)
    const marks = ws.match(/className="rail-tile rail-ws[^"]*"/g) ?? []
    expect(marks.length).toBe(2)
    for (const m of marks) expect(m).not.toMatch(/\bsize-/)
    const css = readFileSync(path.resolve(__dirname, "../../../app/globals.css"), "utf8")
    expect(css).toMatch(/\.rail-tile \{[^}]*width: 28px;[^}]*height: 28px;/)
  })
})
