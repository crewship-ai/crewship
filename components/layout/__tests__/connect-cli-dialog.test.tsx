import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

import { ConnectCliDialog, pairCommand } from "../connect-cli-dialog"

describe("pairCommand", () => {
  it.each([
    ["https://crewship.example.com", "crewship login --pair --code=K3F9X2NM --server=https://crewship.example.com"],
    ["http://localhost:8080", "crewship login --pair --code=K3F9X2NM"],
    ["http://127.0.0.1:8080", "crewship login --pair --code=K3F9X2NM"],
    ["http://localhost:3011", "crewship login --pair --code=K3F9X2NM --server=http://localhost:3011"],
  ])("from %s", (origin, want) => {
    expect(pairCommand("K3F9X2NM", origin)).toBe(want)
  })
})

describe("ConnectCliDialog", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })

  it("asks for a fresh code when opened and shows the command to paste", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ code: "K3F9X2NM", expires_at: new Date(Date.now() + 600_000).toISOString() }),
    })
    render(<ConnectCliDialog open onOpenChange={() => {}} />)
    expect(await screen.findByTestId("pair-command")).toHaveTextContent("crewship login --pair --code=K3F9X2NM")
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/auth/pair/start", expect.objectContaining({ method: "POST" }))
  })

  // G15 (CHR-02): a failed start is an alert with Retry; "Get a new code"
  // belongs to an expired code.
  it("offers a retry when the server refuses", async () => {
    apiFetch.mockResolvedValue({ ok: false, json: () => Promise.resolve({ error: "Too many requests" }) })
    render(<ConnectCliDialog open onOpenChange={() => {}} />)
    expect(await screen.findByText("Too many requests")).toBeInTheDocument()
    expect(screen.getByRole("alert")).toHaveTextContent("Too many requests")
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument()
  })

  // v67 u_sh_cli: a medium form with the terminal tile, "Connect the CLI",
  // and a footer hint plus Done — no Cancel, Done closes in any state.
  it("is the v67 form: titled Connect the CLI, Done closes, no Cancel", async () => {
    apiFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ code: "K3F9X2NM", expires_at: new Date(Date.now() + 600_000).toISOString() }),
    })
    const onOpenChange = vi.fn()
    render(<ConnectCliDialog open onOpenChange={onOpenChange} />)
    const dialog = await screen.findByRole("dialog", { name: "Connect the CLI" })
    expect(within(dialog).getByText(/Connected tokens appear in Sessions & CLI tokens/)).toBeInTheDocument()
    expect(within(dialog).queryByRole("button", { name: "Cancel" })).toBeNull()
    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("pending turns into connected through the poll, with a way to Sessions", async () => {
    let consumed = false
    apiFetch.mockImplementation((url: string) =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve(
          String(url).includes("/poll")
            ? { status: consumed ? "consumed" : "pending" }
            : { code: "K3F9X2NM", expires_at: new Date(Date.now() + 600_000).toISOString() },
        ),
      }),
    )
    render(<ConnectCliDialog open onOpenChange={() => {}} />)
    await screen.findByTestId("pair-command")
    consumed = true
    expect(await screen.findByText(/CLI connected/, {}, { timeout: 4000 })).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Open Sessions" })).toHaveAttribute("href", "/settings?tab=sessions")
  })

  it("makes no request while closed", () => {
    render(<ConnectCliDialog open={false} onOpenChange={() => {}} />)
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
