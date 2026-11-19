import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { RemoveMember } from "../remove-member"
import type { RemoveOutcome } from "../use-members"
import type { Member } from "../members-model"

/**
 * Removing a member says what it costs; when the server answers 409
 * `page_ownership_transfer_required`, the dialog turns into a successor picker
 * and tries again with the hand-over. Without a list (an admin's answer) it
 * says an Owner has to choose and what to do next.
 */

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }))
vi.mock("sonner", () => ({ toast }))
beforeAllShim()
function beforeAllShim() {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.setPointerCapture = vi.fn()
  Element.prototype.releasePointerCapture = vi.fn()
}

const person: Member = { id: "m-ada", role: "MEMBER", created_at: "2026-03-03T10:00:00Z", user: { id: "u-ada", email: "ada@x.io", full_name: "Ada Admin", avatar_url: null } }
const bob = { userId: "u-bob", name: "Bob Builder", email: "bob@x.io" }
const cy = { userId: "u-cy", name: "Cy Cook", email: "cy@x.io" }
const page = (id: string, name: string, successors = [bob, cy]) => ({ id, name, type: "doc", successors })

function setup(outcomes: RemoveOutcome[], handOver: (m: Member, pageId: string, to: string) => Promise<string | null> = async () => null) {
  const tryRemove = vi.fn(async () => outcomes.shift() ?? { ok: true as const })
  const handOverPage = vi.fn(handOver)
  const onRemoved = vi.fn()
  render(<RemoveMember member={person} label="Ada Admin" tryRemove={tryRemove} handOverPage={handOverPage} onRemoved={onRemoved} />)
  fireEvent.click(screen.getByRole("button", { name: "Remove member" }))
  return { tryRemove, handOverPage, onRemoved }
}
const dialog = () => screen.findByTestId("confirm-dialog")
const confirm = async (name: string) => fireEvent.click(within(await dialog()).getByRole("button", { name }))
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe("Remove member", () => {
  it("lists what removing costs, then removes", async () => {
    const { tryRemove, handOverPage, onRemoved } = setup([{ ok: true }])
    const d = await dialog()
    expect(d).toHaveTextContent("Remove Ada Admin?")
    expect(d).toHaveTextContent(/lose this workspace and every crew, routine and artifact/)
    expect(d).toHaveTextContent(/Private artifacts they own need a new owner first/)
    expect(d).toHaveTextContent(/Adding them again starts from the role’s default abilities/)
    await confirm("Remove member")
    await waitFor(() => expect(onRemoved).toHaveBeenCalledWith(person))
    expect(tryRemove).toHaveBeenCalledTimes(1)
    expect(handOverPage).not.toHaveBeenCalled()
  })

  it("asks who takes over the private artifacts, hands them over, and removes again", async () => {
    const { tryRemove, handOverPage, onRemoved } = setup([
      { ok: false, handOver: { message: "x", pages: [page("p1", "Launch plan"), page("p2", "Roadmap")] } },
      { ok: true },
    ])
    await confirm("Remove member")
    const d = await dialog()
    await waitFor(() => expect(d).toHaveTextContent("Ada Admin owns 2 private artifacts"))
    expect(d).toHaveTextContent("Launch plan, Roadmap")
    // Nothing is handed over, nothing removed, until someone is chosen.
    const go = within(d).getByRole("button", { name: "Hand over and remove" })
    expect(go).toBeDisabled()
    fireEvent.click(within(d).getByRole("combobox", { name: "Who takes over their private artifacts" }))
    fireEvent.click(await screen.findByRole("option", { name: /Bob Builder/ }))
    expect(go).toBeEnabled()
    fireEvent.click(go)
    await waitFor(() => expect(onRemoved).toHaveBeenCalledWith(person))
    expect(handOverPage.mock.calls).toEqual([[person, "p1", "u-bob"], [person, "p2", "u-bob"]])
    expect(tryRemove).toHaveBeenCalledTimes(2)
  })

  it("chooses per artifact when nobody can take over all of them", async () => {
    const { handOverPage, onRemoved } = setup([
      { ok: false, handOver: { message: "x", pages: [page("p1", "Launch plan", [bob]), page("p2", "Roadmap", [cy])] } },
      { ok: true },
    ])
    await confirm("Remove member")
    const d = await dialog()
    const go = await within(d).findByRole("button", { name: "Hand over and remove" })
    expect(d).toHaveTextContent("Nobody can take over all of them")
    fireEvent.change(within(d).getByLabelText("Who takes over Launch plan"), { target: { value: "u-bob" } })
    expect(go).toBeDisabled()
    fireEvent.change(within(d).getByLabelText("Who takes over Roadmap"), { target: { value: "u-cy" } })
    fireEvent.click(go)
    await waitFor(() => expect(onRemoved).toHaveBeenCalled())
    expect(handOverPage.mock.calls).toEqual([[person, "p1", "u-bob"], [person, "p2", "u-cy"]])
  })

  it("stops at a refused hand-over, says why, and removes nobody", async () => {
    const { tryRemove, onRemoved } = setup(
      [{ ok: false, handOver: { message: "x", pages: [page("p1", "Launch plan")] } }, { ok: true }],
      async () => "page ownership changed; refresh before transferring",
    )
    await confirm("Remove member")
    const d = await dialog()
    fireEvent.click(await within(d).findByRole("combobox", { name: "Who takes over their private artifacts" }))
    fireEvent.click(await screen.findByRole("option", { name: /Cy Cook/ }))
    fireEvent.click(within(d).getByRole("button", { name: "Hand over and remove" }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Couldn’t hand over “Launch plan”", expect.objectContaining({ description: "page ownership changed; refresh before transferring" })))
    expect(tryRemove).toHaveBeenCalledTimes(1)
    expect(onRemoved).not.toHaveBeenCalled()
    expect(screen.getByTestId("confirm-dialog")).toBeInTheDocument()
  })

  it("tells an admin an Owner has to choose, with the next step, and offers no way to continue", async () => {
    const message = "workspace OWNER must select a private artifact successor before removing this member"
    const { handOverPage, onRemoved } = setup([{ ok: false, handOver: { message, pages: [] } }])
    await confirm("Remove member")
    const d = await dialog()
    await waitFor(() => expect(d).toHaveTextContent(message))
    expect(d.querySelector("[data-slot=remove-owner-needed]")).toHaveTextContent(/Ask an Owner to choose who takes over/)
    expect(d).toHaveTextContent("crewship workspace page-ownership list")
    expect(within(d).getByRole("button", { name: "Hand over and remove" })).toBeDisabled()
    expect(handOverPage).not.toHaveBeenCalled()
    expect(onRemoved).not.toHaveBeenCalled()
  })
})
