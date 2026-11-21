import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { JournalIntegrityDialog, JournalIntegrityLine, useJournalIntegrity } from "../journal-integrity"

// Settings › Audit log: the journal's hash chain can be checked from the page
// head (GET /api/v1/admin/journal/verify, the same check as `crewship journal
// verify`). It runs on request, walks every entry, and the dialog shows the
// whole answer: five states, idle to error.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const json = (status: number, body: unknown) => ({ ok: status >= 200 && status < 300, status, json: async () => body })

function Harness() {
  const integrity = useJournalIntegrity("ws1")
  return (
    <>
      <JournalIntegrityLine integrity={integrity} />
      <JournalIntegrityDialog integrity={integrity} />
    </>
  )
}

beforeEach(() => { cleanup(); apiFetch.mockReset() })
afterEach(() => cleanup())

describe("journal integrity", () => {
  it("idle: does not walk the journal until asked, and says it has not been checked", () => {
    render(<Harness />)
    expect(apiFetch).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: "Check now" })).toBeInTheDocument()
    expect(screen.getByText("Record not checked yet")).toBeInTheDocument()
    expect(screen.queryByRole("dialog")).toBeNull()
    expect(document.querySelector("[data-state=idle]")).not.toBeNull()
  })

  it("running: opens the dialog at once and holds Check now off", async () => {
    let finish!: (v: unknown) => void
    apiFetch.mockReturnValue(new Promise((r) => { finish = r }))
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    expect(await screen.findByRole("dialog")).toBeInTheDocument()
    expect(document.querySelector("[data-slot=journal-integrity][data-state=running]")).not.toBeNull()
    expect(screen.getByRole("button", { name: "Check now", hidden: true })).toBeDisabled()
    expect(apiFetch).toHaveBeenCalledWith("/api/v1/admin/journal/verify?workspace_id=ws1")
    finish(json(200, { ok: true, count: 1 }))
  })

  it("ok: the line says Record intact · checked HH:MM and the dialog the whole answer", async () => {
    apiFetch.mockResolvedValue(json(200, { workspace_id: "ws1", ok: true, count: 48211, checkpoints: 12, continuation_verified: true, historical_status: "verified" }))
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    const dialog = await screen.findByRole("dialog")
    expect(await screen.findByText(/Record intact · checked \d{2}:\d{2}/)).toBeInTheDocument()
    expect(within(dialog).getByText("Record intact")).toBeInTheDocument()
    expect(dialog).toHaveTextContent("48,211")
    expect(dialog).toHaveTextContent("Entries checked")
    expect(dialog).toHaveTextContent(/Earlier checkpoints/)
    expect(dialog).toHaveTextContent("12")
    expect(dialog).toHaveTextContent(/Continuation verified/)
    expect(dialog).toHaveTextContent("Yes")
    expect(dialog).toHaveTextContent(/Historical status/)
    expect(dialog).toHaveTextContent("verified")
    expect(dialog).toHaveTextContent("A break raises an incident on the server")
    expect(document.querySelector("[data-state=ok]")).not.toBeNull()
  })

  it("broken: shows the first break and every break, and says the server raised an incident", async () => {
    apiFetch.mockResolvedValue(json(200, {
      ok: false, count: 50, checkpoints: 1, broken_seq: 42, broken_id: "j_42", reason: "entry hash mismatch", continuation_verified: false,
      break_count: 3, breaks: [
        { seq: 42, id: "j_42", kind: "content", reason: "entry hash mismatch" },
        { seq: 43, id: "j_43", kind: "priority", reason: "stored priority differs" },
      ],
    }))
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    const dialog = await screen.findByRole("dialog")
    expect(await screen.findByText(/Record broken at entry 42 · checked/)).toBeInTheDocument()
    expect(within(dialog).getByText("Record broken")).toBeInTheDocument()
    expect(dialog).toHaveTextContent("#42 · j_42")
    expect(dialog).toHaveTextContent("entry hash mismatch")
    const list = within(dialog).getByTestId("journal-breaks")
    expect(within(list).getAllByRole("listitem")).toHaveLength(2)
    expect(list).toHaveTextContent("stored priority differs")
    expect(list).toHaveTextContent("priority")
    expect(dialog).toHaveTextContent("3 breaks found")
    expect(dialog).toHaveTextContent("Showing the first 2 of 3")
    expect(dialog).toHaveTextContent("The server raised an incident for this break")
    expect(dialog).toHaveTextContent("No")
    expect(document.querySelector("[data-state=broken]")).not.toBeNull()
  })

  it("error: says the check could not run and offers it again", async () => {
    apiFetch.mockResolvedValue(json(500, { error: "verification failed" }))
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    const dialog = await screen.findByRole("dialog")
    expect(await within(dialog).findByText("verification failed")).toBeInTheDocument()
    expect(within(dialog).getByText("Couldn’t check the record")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Check now", hidden: true })).toBeEnabled()
    expect(document.querySelector("[data-state=error]")).not.toBeNull()
  })

  it("error: a network failure says the server could not be reached", async () => {
    apiFetch.mockRejectedValue(new Error("down"))
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    expect(await screen.findByText("The server could not be reached.")).toBeInTheDocument()
  })

  it("the line reopens the dialog once there is a result", async () => {
    apiFetch.mockResolvedValue(json(200, { ok: true, count: 3 }))
    render(<Harness />)
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    fireEvent.click(await screen.findByRole("button", { name: "Done" }))
    await vi.waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
    fireEvent.click(screen.getByRole("button", { name: /Record intact · checked/ }))
    expect(await screen.findByRole("dialog")).toBeInTheDocument()
  })
})
