import { afterEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor, cleanup } from "@testing-library/react"

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
    expect(screen.getByRole("alert")).toHaveTextContent(/complete share ID and token/i)
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
    expect(screen.getByRole("alert")).toHaveTextContent(/complete share ID and token/i)
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
})
