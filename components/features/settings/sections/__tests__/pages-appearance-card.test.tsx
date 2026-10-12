import { beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { PagesAppearanceCard } from "../pages-appearance-card"

const api = vi.fn()
const refresh = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => api(...args) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspacePagesTheme: () => ({ accent: "#123abc" }), refreshWorkspaceSettings: () => refresh() }))
beforeEach(() => { cleanup(); api.mockReset(); refresh.mockReset() })
it("reads workspace colors and saves only the palette through the workspace API", async () => {
 api.mockResolvedValue({ ok: true }); refresh.mockResolvedValue(undefined)
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 expect(screen.getByLabelText("Brand accent")).toHaveValue("#123abc")
 fireEvent.change(screen.getByLabelText("Brand accent"), { target: { value: "#ff6000" } })
 fireEvent.click(screen.getByRole("button", { name: /^save$/i }))
 await waitFor(() => expect(refresh).toHaveBeenCalledOnce())
 expect(api.mock.calls[0][0]).toBe("/api/v1/workspaces/ws1?workspace_id=ws1")
 const body = JSON.parse(api.mock.calls[0][1].body)
 expect(Object.keys(body)).toEqual(["pages_theme"])
 expect(body.pages_theme.accent).toBe("#ff6000")
})
it("keeps an unsuccessful draft and blocks invalid color values", async () => {
 api.mockResolvedValue({ ok: false, json: async () => ({ error: "Permission revoked" }) })
 render(<PagesAppearanceCard workspaceId="ws1" role="OWNER" />)
 fireEvent.change(screen.getByLabelText("Brand accent"), { target: { value: "invalid" } })
 expect(screen.getByRole("button", { name: /^save$/i })).toBeDisabled()
 fireEvent.change(screen.getByLabelText("Brand accent"), { target: { value: "#ff6000" } })
 fireEvent.click(screen.getByRole("button", { name: /^save$/i }))
 await screen.findByText("Permission revoked")
 expect(screen.getByLabelText("Brand accent")).toHaveValue("#ff6000")
 expect(refresh).not.toHaveBeenCalled()
})
it("does not offer write controls to a member", () => {
 render(<PagesAppearanceCard workspaceId="ws1" role="MEMBER" />)
 expect(screen.queryByRole("textbox")).toBeNull()
 expect(screen.queryByLabelText("Brand accent picker")).toBeNull()
 expect(screen.queryByRole("radiogroup", { name: "Start from" })).toBeNull()
 expect(screen.queryByRole("radio")).toBeNull()
 expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull()
 expect(screen.getByText("#123abc")).toBeInTheDocument()
 // The palette is still shown, contrast included.
 expect(screen.getByTestId("pages-mini-preview")).toBeInTheDocument()
 expect(screen.getAllByTestId("contrast-badge")).toHaveLength(4)
})

// "Start from" tiles replace "Use default colors": a tile sets all six
// colours, and the tile whose palette the draft matches is the checked one.
it("sets the whole draft from a preset and marks the matching tile", async () => {
 api.mockResolvedValue({ ok: true }); refresh.mockResolvedValue(undefined)
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 const group = screen.getByRole("radiogroup", { name: "Start from" })
 expect(group).toBeInTheDocument()
 // The saved accent (#123abc) is not any preset's.
 for (const name of ["Mint", "Midnight blue", "Paper", "Graphite"]) expect(screen.getByRole("radio", { name })).toHaveAttribute("aria-checked", "false")
 fireEvent.click(screen.getByRole("radio", { name: "Paper" }))
 expect(screen.getByRole("radio", { name: "Paper" })).toHaveAttribute("aria-checked", "true")
 expect(screen.getByRole("radio", { name: "Mint" })).toHaveAttribute("aria-checked", "false")
 const paper = { "Brand accent": "#2563eb", Background: "#fafaf7", Cards: "#ffffff", Text: "#1c2430", "Secondary text": "#566072", Borders: "#e2e4e8" }
 for (const [label, value] of Object.entries(paper)) expect(screen.getByLabelText(label)).toHaveValue(value)
 // Editing one colour leaves the preset.
 fireEvent.change(screen.getByLabelText("Cards"), { target: { value: "#fefefe" } })
 expect(screen.getByRole("radio", { name: "Paper" })).toHaveAttribute("aria-checked", "false")
 // Mint is the default palette.
 fireEvent.click(screen.getByRole("radio", { name: "Mint" }))
 expect(screen.getByRole("radio", { name: "Mint" })).toHaveAttribute("aria-checked", "true")
 expect(screen.getByLabelText("Brand accent")).toHaveValue("#9fe5bd")
 expect(screen.getByLabelText("Background")).toHaveValue("#0c1410")
 fireEvent.click(screen.getByRole("radio", { name: "Graphite" }))
 fireEvent.click(screen.getByRole("button", { name: /^save$/i }))
 await waitFor(() => expect(refresh).toHaveBeenCalledOnce())
 expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({ pages_theme: { accent: "#5eead4", background: "#141516", surface: "#1d1e20", text: "#ececed", muted: "#a1a1a8", border: "#323438" } })
})

it("shows live contrast of each text colour on background and cards", () => {
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 expect(screen.getByLabelText("Contrast on background: AA 16.8:1")).toHaveTextContent("AA 16.8:1")
 expect(screen.getByLabelText("Contrast on cards: AA 15.7:1")).toBeInTheDocument()
 expect(screen.queryByText(/below 4\.5:1/)).toBeNull()
 fireEvent.change(screen.getByLabelText("Text"), { target: { value: "#444444" } })
 const badges = screen.getAllByTestId("contrast-badge").map(b => b.textContent)
 expect(badges[0]).toMatch(/^Low \d+\.\d:1$/)
 expect(badges[0]).toBe("Low 1.9:1")
 expect(screen.getByText(/below 4\.5:1/)).toHaveAttribute("role", "status")
})

// "Preview" opens a half-screen panel with a sample Page application painted
// from the unsaved draft, so an admin sees a palette before saving it; it
// follows edits live and closes without saving anything.
it("previews the unsaved palette on a sample page, live, and closes without saving", async () => {
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 fireEvent.click(screen.getByRole("button", { name: /^preview$/i }))
 const page = await screen.findByTestId("pages-theme-preview")
 expect(page.style.getPropertyValue("--crewship-page-accent")).toBe("#123abc")
 fireEvent.change(screen.getByLabelText("Background"), { target: { value: "#f4f7fb" } })
 await waitFor(() => expect(screen.getByTestId("pages-theme-preview").style.getPropertyValue("--crewship-page-background")).toBe("#f4f7fb"))
 fireEvent.click(screen.getByRole("button", { name: /close preview/i }))
 await waitFor(() => expect(screen.queryByTestId("pages-theme-preview")).toBeNull())
 expect(api).not.toHaveBeenCalled()
})

it("offers the preview to a member too, read-only", async () => {
 render(<PagesAppearanceCard workspaceId="ws1" role="MEMBER" />)
 fireEvent.click(screen.getByRole("button", { name: /^preview$/i }))
 expect(await screen.findByTestId("pages-theme-preview")).toBeInTheDocument()
})
