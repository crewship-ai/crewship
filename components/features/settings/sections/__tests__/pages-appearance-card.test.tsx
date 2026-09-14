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
 expect(screen.queryByRole("button", { name: /default colors/i })).toBeNull()
 expect(screen.getByText("#123abc")).toBeInTheDocument()
})
