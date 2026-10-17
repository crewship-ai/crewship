import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import { JournalVerifyBanner } from "../journal-verify-banner"

// Settings › Audit log: the journal's hash chain can be checked from the page
// (GET /api/v1/admin/journal/verify, the same check as `crewship journal
// verify`). It runs on request — it walks every entry.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

function json(status: number, body: unknown) {
  return { ok: status >= 200 && status < 300, status, json: async () => body }
}

beforeEach(() => {
  cleanup()
  apiFetch.mockReset()
})

describe("JournalVerifyBanner", () => {
  it("does not walk the journal until asked", () => {
    render(<JournalVerifyBanner workspaceId="ws1" />)
    expect(apiFetch).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "Verify now" })).toBeInTheDocument()
  })

  it("ok: says the chain is intact and how many entries were checked", async () => {
    apiFetch.mockResolvedValue(json(200, { workspace_id: "ws1", ok: true, count: 1234 }))
    render(<JournalVerifyBanner workspaceId="ws1" />)
    fireEvent.click(screen.getByRole("button", { name: "Verify now" }))
    expect(await screen.findByText(/Journal chain verified/)).toBeInTheDocument()
    expect(screen.getByText(/1,234 entries/)).toBeInTheDocument()
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/admin/journal/verify?workspace_id=ws1")
    expect(document.querySelector("[data-state=ok]")).not.toBeNull()
  })

  it("broken: names the first bad entry and why", async () => {
    apiFetch.mockResolvedValue(json(200, { workspace_id: "ws1", ok: false, count: 50, broken_seq: 42, reason: "entry hash mismatch" }))
    render(<JournalVerifyBanner workspaceId="ws1" />)
    fireEvent.click(screen.getByRole("button", { name: "Verify now" }))
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(/Journal chain broken at entry 42/)
    expect(alert).toHaveTextContent(/entry hash mismatch/)
    expect(document.querySelector("[data-state=broken]")).not.toBeNull()
  })

  it("error: says the check could not run and offers it again", async () => {
    apiFetch.mockResolvedValue(json(500, { error: "verification failed" }))
    render(<JournalVerifyBanner workspaceId="ws1" />)
    fireEvent.click(screen.getByRole("button", { name: "Verify now" }))
    expect(await screen.findByText(/Couldn’t verify the journal chain/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Verify now" })).toBeInTheDocument()
    expect(document.querySelector("[data-state=error]")).not.toBeNull()
  })
})
