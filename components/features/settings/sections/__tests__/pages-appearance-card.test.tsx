import { beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { PagesAppearanceCard } from "../pages-appearance-card"

const api = vi.fn()
const refresh = vi.fn()
let saved: unknown = { accent: "#123abc" }
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => api(...args) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspacePagesTheme: () => saved, refreshWorkspaceSettings: () => refresh() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
beforeEach(() => { cleanup(); api.mockReset(); refresh.mockReset(); saved = { accent: "#123abc" } })

it("reads workspace colors and saves only the palette through the workspace API", async () => {
 api.mockResolvedValue({ ok: true }); refresh.mockResolvedValue(undefined)
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 expect(screen.getByLabelText("Accent")).toHaveValue("#123abc")
 fireEvent.change(screen.getByLabelText("Accent"), { target: { value: "#ff6000" } })
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
 fireEvent.change(screen.getByLabelText("Accent"), { target: { value: "invalid" } })
 expect(screen.getByRole("button", { name: /^save$/i })).toBeDisabled()
 fireEvent.change(screen.getByLabelText("Accent"), { target: { value: "#ff6000" } })
 fireEvent.click(screen.getByRole("button", { name: /^save$/i }))
 await screen.findByText("Permission revoked")
 expect(screen.getByLabelText("Accent")).toHaveValue("#ff6000")
 expect(refresh).not.toHaveBeenCalled()
})

it("does not offer write controls to a member", () => {
 render(<PagesAppearanceCard workspaceId="ws1" role="MEMBER" />)
 expect(screen.queryByRole("textbox")).toBeNull()
 expect(screen.queryByLabelText("Accent picker")).toBeNull()
 expect(screen.queryByRole("radio")).toBeNull()
 expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull()
 expect(screen.queryByRole("button", { name: /reset to crewship default/i })).toBeNull()
 expect(screen.getByText("#123abc")).toBeInTheDocument()
 // The palette is still shown, contrast included: one badge per text colour.
 expect(screen.getByTestId("pages-mini-preview")).toBeInTheDocument()
 expect(screen.getAllByTestId("contrast-badge")).toHaveLength(2)
})

// A workspace that never customised stores `{}` and wears the Crewship
// palette — the product's own dark theme with its brand blue.
it("defaults to the Crewship palette and says so", () => {
 saved = {}
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 expect(screen.getByTestId("pages-theme-status")).toHaveTextContent("Crewship default")
 expect(screen.getByRole("radio", { name: "Crewship" })).toHaveAttribute("aria-checked", "true")
 expect(screen.getByLabelText("Accent")).toHaveValue("#1e7bfe")
 expect(screen.getByLabelText("Background")).toHaveValue("#0a0b0e")
 // Nothing to reset when already following the default.
 expect(screen.queryByRole("button", { name: /reset to crewship default/i })).toBeNull()
})

it("sets the whole draft from a preset, marks it, and falls back to Custom on an edit", async () => {
 api.mockResolvedValue({ ok: true }); refresh.mockResolvedValue(undefined)
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 // The saved accent (#123abc) is no preset's, so Custom is the pressed tile.
 expect(screen.getByRole("radio", { name: "Custom" })).toHaveAttribute("aria-checked", "true")
 expect(screen.getByTestId("pages-theme-status")).toHaveTextContent("Custom")
 fireEvent.click(screen.getByRole("radio", { name: "Paper" }))
 expect(screen.getByRole("radio", { name: "Paper" })).toHaveAttribute("aria-checked", "true")
 expect(screen.getByRole("radio", { name: "Custom" })).toHaveAttribute("aria-checked", "false")
 const paper = { Accent: "#1d4ed8", Background: "#fbfaf7", Cards: "#ffffff", Text: "#1c2430", "Secondary text": "#566072", Borders: "#e7e3da" }
 for (const [label, value] of Object.entries(paper)) expect(screen.getByLabelText(label)).toHaveValue(value)
 fireEvent.change(screen.getByLabelText("Cards"), { target: { value: "#fefefe" } })
 expect(screen.getByRole("radio", { name: "Paper" })).toHaveAttribute("aria-checked", "false")
 expect(screen.getByRole("radio", { name: "Custom" })).toHaveAttribute("aria-checked", "true")
 fireEvent.click(screen.getByRole("radio", { name: "Midnight" }))
 fireEvent.click(screen.getByRole("button", { name: /^save$/i }))
 await waitFor(() => expect(refresh).toHaveBeenCalledOnce())
 expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({ pages_theme: { accent: "#6ea8ff", background: "#0b1220", surface: "#141e33", text: "#e6edf7", muted: "#9fb0c8", border: "#27344f" } })
})

it("resets a custom palette to the Crewship default by saving an empty one", async () => {
 api.mockResolvedValue({ ok: true }); refresh.mockResolvedValue(undefined)
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 fireEvent.click(screen.getByRole("button", { name: /reset to crewship default/i }))
 await waitFor(() => expect(refresh).toHaveBeenCalledOnce())
 expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({ pages_theme: {} })
})

it("shows one contrast badge per text colour, low when either surface fails", () => {
 render(<PagesAppearanceCard workspaceId="ws1" role="ADMIN" />)
 const [text] = screen.getAllByTestId("contrast-badge")
 expect(text).toHaveTextContent("AA")
 expect(text.getAttribute("title")).toMatch(/:1 on background · .*:1 on cards/)
 expect(screen.queryByText(/below 4\.5:1/)).toBeNull()
 fireEvent.change(screen.getByLabelText("Text"), { target: { value: "#444444" } })
 expect(screen.getAllByTestId("contrast-badge")[0]).toHaveTextContent("Low")
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
 fireEvent.change(screen.getAllByLabelText("Background")[0], { target: { value: "#f4f7fb" } })
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
