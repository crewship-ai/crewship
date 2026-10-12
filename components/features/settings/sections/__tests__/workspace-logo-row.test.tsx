import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({ api: vi.fn(), toast: { success: vi.fn(), error: vi.fn() }, refresh: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.api(...a) }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/hooks/use-workspace", () => ({ refreshWorkspaceSettings: () => h.refresh() }))

import { WorkspaceLogoRow } from "../workspace-logo-row"

// Settings › General › Workspace logo (#3005): the profile picture's twin —
// PNG, JPEG or WebP up to 2MB, uploaded at once, removable; read-only below ADMIN.
const png = () => new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "logo.png", { type: "image/png" })

beforeEach(() => {
  h.api.mockReset()
  h.toast.success.mockReset()
  h.toast.error.mockReset()
  h.refresh.mockReset()
})
afterEach(() => cleanup())

describe("WorkspaceLogoRow", () => {
  it("uploads a picked file to the workspace's logo endpoint and shows it", async () => {
    h.api.mockResolvedValue(new Response(JSON.stringify({ logo_url: "/api/v1/workspaces/w1/logo?v=2" }), { status: 200 }))
    const onChange = vi.fn()
    const { container } = render(<WorkspaceLogoRow workspaceId="w1" name="Dess" logoUrl={null} canEdit onChange={onChange} />)
    fireEvent.change(screen.getByLabelText("Upload workspace logo"), { target: { files: [png()] } })
    await waitFor(() => expect(h.api).toHaveBeenCalledWith("/api/v1/workspaces/w1/logo", expect.objectContaining({ method: "POST" })))
    await waitFor(() => expect(container.querySelector("img")?.getAttribute("src")).toBe("/api/v1/workspaces/w1/logo?v=2"))
    expect(onChange).toHaveBeenCalledWith("/api/v1/workspaces/w1/logo?v=2")
    expect(h.refresh).toHaveBeenCalled()
  })

  it("refuses a file that is not an image before sending it", () => {
    render(<WorkspaceLogoRow workspaceId="w1" name="Dess" logoUrl={null} canEdit onChange={vi.fn()} />)
    fireEvent.change(screen.getByLabelText("Upload workspace logo"), { target: { files: [new File(["x"], "a.txt", { type: "text/plain" })] } })
    expect(h.api).not.toHaveBeenCalled()
    expect(screen.getByText("Must be a PNG, JPEG, or WebP image")).toBeInTheDocument()
  })

  it("removes the logo", async () => {
    h.api.mockResolvedValue(new Response(JSON.stringify({ logo_url: null }), { status: 200 }))
    const onChange = vi.fn()
    render(<WorkspaceLogoRow workspaceId="w1" name="Dess" logoUrl="/x.png" canEdit onChange={onChange} />)
    fireEvent.click(screen.getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(h.api).toHaveBeenCalledWith("/api/v1/workspaces/w1/logo", { method: "DELETE" }))
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(null))
  })

  it("is read-only below ADMIN: the logo, no controls", () => {
    render(<WorkspaceLogoRow workspaceId="w1" name="Dess" logoUrl="/x.png" canEdit={false} onChange={vi.fn()} />)
    expect(screen.queryByRole("button", { name: /Upload|Change|Remove/ })).toBeNull()
  })
})
