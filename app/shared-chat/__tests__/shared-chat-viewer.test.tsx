import { afterEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor, cleanup, within } from "@testing-library/react"

import { SharedChatViewer } from "../shared-chat-viewer"

const shareID = `cshr_${"a".repeat(32)}`
const token = `cshr_${"B".repeat(43)}`

function fillCredentials() {
  fireEvent.change(screen.getByLabelText("Share ID"), { target: { value: shareID } })
  fireEvent.change(screen.getByLabelText("Share token"), { target: { value: token } })
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("shared chat viewer", () => {
  it("sends the secret only as an isolated bearer and renders transcript text literally", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ messages: [
      { id: "m1", role: "assistant", content: "<img src=x onerror=alert(1)>\nplain text", created_at: "2026-09-27T00:00:00Z" },
    ] }), { status: 200 }))
    vi.stubGlobal("fetch", fetchMock)
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))

    await waitFor(() => expect(screen.getByText(/<img src=x onerror=alert\(1\)>/)).toBeInTheDocument())
    expect(document.querySelector("img")).toBeNull()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`/api/v1/shared-chats/${shareID}/messages`)
    expect(url).not.toContain(token)
    expect(init.headers).toMatchObject({ Authorization: `Bearer ${token}` })
    expect(init).toMatchObject({ credentials: "omit", cache: "no-store", redirect: "error", referrerPolicy: "no-referrer" })
    expect(screen.getByText(/future messages until it expires or is revoked/i)).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    fireEvent.click(screen.getByRole("button", { name: "Change" }))
    fireEvent.click(screen.getByRole("button", { name: "Clear" }))
    expect(screen.getByLabelText("Share ID")).toHaveValue("")
    expect(screen.getByLabelText("Share token")).toHaveValue("")
    expect(screen.queryByLabelText("Shared transcript")).toBeNull()
  })

  it("keeps missing or revoked shares as a visible error without echoing the token", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("{}", { status: 404 }))
    vi.stubGlobal("fetch", fetchMock)
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(/share is unavailable/i)
    expect(alert).not.toHaveTextContent(token)
    expect(screen.queryByLabelText("Shared transcript")).toBeNull()
  })

  it("does not send incomplete credentials or render unexpected response fields", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ messages: [
      { id: "m1", role: "tool", content: "secret", created_at: "2026-09-27T00:00:00Z" },
    ] }), { status: 200 }))
    vi.stubGlobal("fetch", fetchMock)
    render(<SharedChatViewer />)
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    // Each malformed value is named under its own field.
    const alerts = screen.getAllByRole("alert")
    expect(alerts.map((a) => a.id)).toEqual(["share-id-error", "share-token-error"])
    expect(alerts[0]).toHaveTextContent(/complete share ID/i)
    expect(alerts[1]).toHaveTextContent(/complete share token/i)
    expect(screen.getByLabelText("Share ID")).toHaveAttribute("aria-invalid", "true")
    expect(fetchMock).not.toHaveBeenCalled()

    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/transcript response was invalid/i)
    expect(screen.queryByText("secret")).toBeNull()
  })

  it("does not serialize credentials into native form submission and ignores a stale read after invalid input", async () => {
    let finish!: (response: Response) => void
    const fetchMock = vi.fn().mockImplementation(() => new Promise<Response>((resolve) => { finish = resolve }))
    vi.stubGlobal("fetch", fetchMock)
    render(<SharedChatViewer />)
    fillCredentials()
    const form = screen.getByLabelText("Share ID").closest("form")!
    expect(new FormData(form).has("share-token")).toBe(false)
    expect(new FormData(form).has("share-id")).toBe(false)
    fireEvent.submit(form)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    fireEvent.change(screen.getByLabelText("Share token"), { target: { value: "incomplete" } })
    fireEvent.submit(form)
    expect(screen.getByRole("alert")).toHaveTextContent(/complete share token/i)
    finish(new Response(JSON.stringify({ messages: [
      { id: "old", role: "assistant", content: "stale secret", created_at: "2026-09-27T00:00:00Z" },
    ] }), { status: 200 }))
    await Promise.resolve()
    expect(screen.queryByText("stale secret")).toBeNull()
  })

  it("uses a controlled error for network failures", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("internal credential detail")))
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("The transcript could not be opened.")
    expect(alert).not.toHaveTextContent("internal credential detail")
  })

  it("explains the transcript size limit", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 413 })))
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    expect(await screen.findByRole("alert")).toHaveTextContent(/over 16 MiB or 1,000 messages/i)
  })

  it.each([
    [401, "not-available", false],
    [404, "not-available", false],
    [413, "error", false],
    [500, "error", true],
    [503, "error", true],
    ["invalid", "error", true],
  ] as const)("maps %s to the %s state (Retry offered: %s)", async (status, kind, retry) => {
    const response = status === "invalid"
      ? new Response(JSON.stringify({ nope: true }), { status: 200 })
      : new Response("{}", { status })
    const fetchMock = vi.fn().mockResolvedValue(response)
    vi.stubGlobal("fetch", fetchMock)
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    const alert = await screen.findByRole("alert")
    const stateEl = alert.matches("[data-state]") ? alert : alert.querySelector("[data-state]")
    expect(stateEl?.getAttribute("data-state")).toBe(kind === "not-available" ? "not-found" : "error")
    if (kind === "not-available") expect(alert).toHaveTextContent("This share is not available")
    expect(screen.queryByRole("button", { name: "Retry" }) !== null).toBe(retry)
    if (retry) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ messages: [] }), { status: 200 }))
      fireEvent.click(screen.getByRole("button", { name: "Retry" }))
      expect(await screen.findByText("No text messages yet")).toBeInTheDocument()
      expect(fetchMock).toHaveBeenCalledTimes(2)
    }
    expect(alert).not.toHaveTextContent(token)
  })

  it("sits under the public bar with no account controls", () => {
    render(<SharedChatViewer />)
    const bar = screen.getByRole("banner")
    expect(bar).toHaveTextContent("Shared chat")
    expect(bar.querySelectorAll("a, button")).toHaveLength(0)
  })

  async function openWith(messages: unknown[]) {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ messages }), { status: 200 })))
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    await screen.findByLabelText("Shared transcript")
  }

  const rows = [
    { id: "m1", role: "user", content: "Where is the report?", created_at: "2026-09-27T08:05:00Z" },
    { id: "m2", role: "assistant", content: "It is attached.", created_at: "2026-09-27T08:06:00Z" },
  ]

  // ── P06: v67 message row ──────────────────────────────────────────────────

  it("draws each message as a row: 30 px avatar, who, time, text", async () => {
    await openWith(rows)
    const items = document.querySelectorAll('[data-slot="shared-message"]')
    expect(items).toHaveLength(2)

    const person = items[0] as HTMLElement
    expect(person.querySelector('[data-slot="shared-message-who"]')).toHaveTextContent("Person")
    const personAvatar = person.querySelector('[data-slot="shared-message-avatar"]') as HTMLElement
    expect(personAvatar.className).toContain("size-[30px]")
    // The API sends no name, so the person is the initial P.
    expect(personAvatar).toHaveTextContent("P")
    const time = person.querySelector("time") as HTMLTimeElement
    expect(time.getAttribute("datetime")).toBe("2026-09-27T08:05:00Z")
    expect(time.textContent).not.toBe("")
    expect(person).toHaveTextContent("Where is the report?")

    const agent = items[1] as HTMLElement
    expect(agent.querySelector('[data-slot="shared-message-who"]')).toHaveTextContent("Agent")
    const agentAvatar = agent.querySelector('[data-slot="shared-message-avatar"]') as HTMLElement
    // A neutral bot tile: a glyph, no letter, because the agent has no name here.
    expect(agentAvatar.querySelector("svg")).not.toBeNull()
    expect(agentAvatar.textContent).toBe("")
  })

  it("keeps the 760 px reading column and a 16 px gutter on a phone", async () => {
    await openWith(rows)
    const column = document.querySelector('[data-slot="shared-transcript-column"]') as HTMLElement
    expect(column.className).toContain("max-w-[760px]")
    const row = document.querySelector('[data-slot="shared-message"]') as HTMLElement
    expect(row.className).toContain("px-4")
    expect(row.className).toContain("md:px-6")
    // Rows are not cards any more.
    expect(document.querySelector("article")).toBeNull()
  })

  // ── P06: the form folds into one line above the transcript ─────────────────

  it("folds the form into 'cshr_… · Change' once the transcript is open, without showing the token", async () => {
    await openWith(rows)
    expect(screen.queryByLabelText("Share token")).toBeNull()
    expect(screen.queryByLabelText("Share ID")).toBeNull()
    const summary = document.querySelector('[data-slot="shared-chat-summary"]') as HTMLElement
    expect(summary).toHaveTextContent(shareID.slice(0, 13))
    expect(summary).not.toHaveTextContent(shareID)
    expect(summary).not.toHaveTextContent(token)
    fireEvent.click(screen.getByRole("button", { name: "Change" }))
    expect(screen.getByLabelText("Share ID")).toHaveValue(shareID)
    expect(screen.getByLabelText("Share token")).toHaveValue(token)
  })

  it("Try another share on the not-available state reopens the form on Share ID", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 404 })))
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    const alert = await screen.findByRole("alert")
    fireEvent.click(within(alert).getByRole("button", { name: "Try another share" }))
    expect(screen.getByLabelText("Share ID")).toHaveFocus()
    expect(screen.queryByText("This share is not available")).toBeNull()
  })

  it("shows the transcript's loading as four avatar rows in the reading column", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => new Promise(() => {})))
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    await waitFor(() => expect(document.querySelector('[data-state="loading"]')).not.toBeNull())
    const state = document.querySelector('[data-state="loading"]') as HTMLElement
    expect(state.querySelectorAll(".page-sk-av")).toHaveLength(4)
    expect(state.closest('[data-slot="shared-transcript-column"]')).not.toBeNull()
  })

  it("offers Copy details on an invalid answer and on nothing else", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ nope: true }), { status: 200 })))
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    const alert = await screen.findByRole("alert")
    expect(within(alert).getByRole("button", { name: "Copy details" })).toBeTruthy()
    cleanup()

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 503 })))
    render(<SharedChatViewer />)
    fillCredentials()
    fireEvent.click(screen.getByRole("button", { name: "Open transcript" }))
    const second = await screen.findByRole("alert")
    expect(within(second).queryByRole("button", { name: "Copy details" })).toBeNull()
  })
})
