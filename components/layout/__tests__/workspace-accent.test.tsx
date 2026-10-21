import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, cleanup } from "@testing-library/react"

let state: { loading: boolean; workspace: { id: string } | null; theme: unknown } = { loading: false, workspace: { id: "ws" }, theme: undefined }
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspace: state.workspace, workspaceId: state.workspace?.id ?? null, loading: state.loading }),
  useWorkspacePagesTheme: () => state.theme,
}))

import { WorkspaceAccentStyle } from "../workspace-accent"
import { WORKSPACE_ACCENT_CSS_KEY, WORKSPACE_ACCENT_STYLE_ID } from "@/lib/theme/accents"
import { workspaceAccentCss } from "@/lib/theme/derive-accent"

let store: Map<string, string>
beforeEach(() => {
  store = new Map()
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  })
  document.getElementById(WORKSPACE_ACCENT_STYLE_ID)?.remove()
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const style = () => document.getElementById(WORKSPACE_ACCENT_STYLE_ID)

describe("WorkspaceAccentStyle", () => {
  it("paints and caches the derived accent of the workspace's brand colour", () => {
    state = { loading: false, workspace: { id: "ws" }, theme: { accent: "#e11d48" } }
    render(<WorkspaceAccentStyle />)
    expect(style()?.textContent).toBe(workspaceAccentCss("#E11D48"))
    expect(store.get(WORKSPACE_ACCENT_CSS_KEY)).toBe(workspaceAccentCss("#E11D48"))
  })

  it("leaves the boot script's stylesheet alone while the workspace is loading", () => {
    const pre = document.createElement("style")
    pre.id = WORKSPACE_ACCENT_STYLE_ID
    pre.textContent = "cached"
    document.head.appendChild(pre)
    state = { loading: true, workspace: null, theme: undefined }
    render(<WorkspaceAccentStyle />)
    expect(style()?.textContent).toBe("cached")
  })

  it("removes the rules and the cache when the workspace has no brand colour", () => {
    store.set(WORKSPACE_ACCENT_CSS_KEY, "old")
    const pre = document.createElement("style")
    pre.id = WORKSPACE_ACCENT_STYLE_ID
    document.head.appendChild(pre)
    state = { loading: false, workspace: { id: "ws" }, theme: undefined }
    render(<WorkspaceAccentStyle />)
    expect(style()).toBeNull()
    expect(store.has(WORKSPACE_ACCENT_CSS_KEY)).toBe(false)
  })

  it("ignores a malformed colour", () => {
    state = { loading: false, workspace: { id: "ws" }, theme: { accent: "red; background:url(x)" } }
    render(<WorkspaceAccentStyle />)
    expect(style()).toBeNull()
  })
})
