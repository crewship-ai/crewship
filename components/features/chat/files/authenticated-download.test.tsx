import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi, afterEach } from "vitest"
import { AuthenticatedDownload } from "./authenticated-download"
import { apiFetch } from "@/lib/api-fetch"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/server-base", () => ({ getAuthMode: () => "bearer" }))
afterEach(() => vi.restoreAllMocks())
describe("authenticated downloads", () => {
  it("uses the bearer-aware API path and downloads beyond the preview limit", async () => {
    const body = new Blob([new Uint8Array(21 * 1024 * 1024)])
    vi.mocked(apiFetch).mockResolvedValue({ ok: true, blob: async () => body } as Response)
    const object = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:download")
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    render(<AuthenticatedDownload href="/api/v1/agents/a/files/download?path=report.pdf" download="report.pdf">Download</AuthenticatedDownload>)
    fireEvent.click(screen.getByRole("link", { name: "Download" }))
    await waitFor(() => expect(object).toHaveBeenCalled())
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/agents/a/files/download?path=report.pdf", { signal: expect.any(AbortSignal) })
    expect((object.mock.calls[0][0] as Blob).size).toBe(21 * 1024 * 1024)
  })
})
