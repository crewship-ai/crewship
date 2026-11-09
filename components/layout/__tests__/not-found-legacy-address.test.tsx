import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: nav.replace, push: vi.fn() }), usePathname: () => window.location.pathname }))
vi.mock("@/lib/store", () => ({ useAppStore: (pick: (s: { openCommandPalette: () => void }) => unknown) => pick({ openCommandPalette: vi.fn() }) }))

import { NotFoundInShell } from "../not-found-state"

afterEach(() => { cleanup(); nav.replace.mockReset(); window.history.replaceState(null, "", "/") })

it("an old /pages/<slug>/editor address moves to the editor address instead of saying it doesn't exist (#G04)", () => {
  window.history.replaceState(null, "", "/pages/brief/editor?workspace_id=ws1")
  render(<NotFoundInShell />)
  expect(nav.replace).toHaveBeenCalledWith("/pages/brief?workspace_id=ws1&mode=edit")
  expect(screen.queryByText("This page doesn’t exist")).toBeNull()
})

it("any other unknown path still says it doesn't exist", () => {
  window.history.replaceState(null, "", "/nowhere/at-all")
  render(<NotFoundInShell />)
  expect(nav.replace).not.toHaveBeenCalled()
  expect(screen.getByText("This page doesn’t exist")).toBeTruthy()
})
