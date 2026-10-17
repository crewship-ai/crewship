import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, act } from "@testing-library/react"

import { apiFetch } from "@/lib/api-fetch"

// CHR-03 (#3482, chrome-states.md G05): the not-found page's Search opens
// the palette with the missing path already typed, so the person searches
// for what they meant. The palette had no initial-query option.
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", role: "OWNER" }) }))
vi.mock("@/hooks/use-auth", () => ({
  useIsInstanceAdmin: () => true,
  useSessionSafe: () => ({ data: { user: { id: "u-test" } }, status: "authenticated" }),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(), broadcastSessionExpired: vi.fn() }))

import * as Palette from "../command-palette"
import { useAppStore } from "@/lib/store"
import { NotFoundInShell, paletteQueryForPath } from "@/components/layout/not-found-state"

type PaletteProps = { open: boolean; onOpenChange: (open: boolean) => void; initialQuery?: string }
const CommandPalette = Palette.CommandPalette as unknown as React.ComponentType<PaletteProps>

beforeEach(() => {
  vi.mocked(apiFetch).mockImplementation(async () => ({ ok: true, json: async () => [] }) as unknown as Response)
  act(() => useAppStore.getState().setCommandPaletteOpen(false))
})
afterEach(cleanup)

describe("palette initial query", () => {
  it("opens with the given query typed in", () => {
    render(<CommandPalette open onOpenChange={vi.fn()} initialQuery="routnes weekly" />)
    expect(screen.getByRole("combobox")).toHaveValue("routnes weekly")
  })

  it("opens empty without one", () => {
    render(<CommandPalette open onOpenChange={vi.fn()} />)
    expect(screen.getByRole("combobox")).toHaveValue("")
  })

  it("turns a missing path into words to search", () => {
    expect(paletteQueryForPath("/routnes/weekly-scan")).toBe("routnes weekly scan")
    expect(paletteQueryForPath("/")).toBe("")
    expect(paletteQueryForPath("/issues/OPS%2042/")).toBe("issues OPS 42")
  })

  it("not found: Search opens the palette with the path typed in", () => {
    window.history.replaceState({}, "", "/routnes/weekly-scan")
    render(<NotFoundInShell />)
    fireEvent.click(screen.getByRole("button", { name: "Search" }))
    const s = useAppStore.getState() as unknown as { commandPaletteOpen: boolean; commandPaletteQuery?: string }
    expect(s.commandPaletteOpen).toBe(true)
    expect(s.commandPaletteQuery).toBe("routnes weekly scan")
    window.history.replaceState({}, "", "/")
  })
})
