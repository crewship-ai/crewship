import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

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

  it("offers a retry when the server refuses", async () => {
    apiFetch.mockResolvedValue({ ok: false, json: () => Promise.resolve({ error: "Too many requests" }) })
    render(<ConnectCliDialog open onOpenChange={() => {}} />)
    expect(await screen.findByText("Too many requests")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /get a new code/i })).toBeInTheDocument()
  })

  it("makes no request while closed", () => {
    render(<ConnectCliDialog open={false} onOpenChange={() => {}} />)
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
