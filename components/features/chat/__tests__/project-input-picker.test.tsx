import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { ProjectInputPicker } from "../files/project-input-picker"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
const own = { version_id: "own-version", name: "Own brief.txt", project_name: "Own project", size_bytes: 12 }
const success = (files = [own]) => ({ ok: true, json: async () => ({ files, has_more: false }) })

beforeEach(() => fetchMock.mockReset())
describe("native project input picker", () => {
  it("starts empty, fetches no bytes, and forwards an explicit version selection", async () => {
    fetchMock.mockResolvedValue(success())
    const onChange = vi.fn()
    render(<ProjectInputPicker chatId="private-chat" workspaceId="w" userId="h1" selected={[]} onChange={onChange} refreshKey={0} />)
    const checkbox = await screen.findByRole("checkbox")
    expect(checkbox).not.toBeChecked()
    expect(screen.getByText("Own brief.txt")).toBeInTheDocument()
    expect(screen.queryByText("own-version")).not.toBeInTheDocument()
    fireEvent.click(checkbox)
    expect(onChange).toHaveBeenLastCalledWith(["own-version"])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toContain("/chats/private-chat/project-input-options?workspace_id=w")
  })

  it("removes old human metadata immediately and ignores the aborted response", async () => {
    let complete!: (response: ReturnType<typeof success>) => void
    fetchMock.mockImplementationOnce(() => new Promise(resolve => { complete = resolve }))
    fetchMock.mockResolvedValueOnce(success([{ ...own, version_id: "h2-version", name: "H2 brief.txt" }]))
    const onChange = vi.fn()
    const { rerender } = render(<ProjectInputPicker chatId="chat1" workspaceId="w" userId="h1" selected={["own-version"]} onChange={onChange} refreshKey={0} />)
    rerender(<ProjectInputPicker chatId="chat2" workspaceId="w" userId="h2" selected={[]} onChange={onChange} refreshKey={0} />)
    expect(onChange).toHaveBeenLastCalledWith([])
    await screen.findByText("H2 brief.txt")
    complete(success())
    await waitFor(() => expect(screen.queryByText("Own brief.txt")).not.toBeInTheDocument())
    expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true)
  })

  it("clears selections when access or the native profile is unavailable", async () => {
    fetchMock.mockResolvedValueOnce(success()).mockResolvedValueOnce({ ok: false, status: 404 })
    const onChange = vi.fn()
    const { rerender } = render(<ProjectInputPicker chatId="chat" workspaceId="w" userId="h1" selected={[]} onChange={onChange} refreshKey={0} />)
    await screen.findByText("Own brief.txt")
    rerender(<ProjectInputPicker chatId="chat" workspaceId="w" userId="h1" selected={["own-version"]} onChange={onChange} refreshKey={1} />)
    await waitFor(() => expect(screen.queryByText("Own brief.txt")).not.toBeInTheDocument())
    expect(onChange).toHaveBeenLastCalledWith([])
  })

  it("enforces 16 explicit selections and refresh clears a stale selection", async () => {
    fetchMock.mockResolvedValue(success(Array.from({ length: 17 }, (_, index) => ({ ...own, version_id: `version${index}`, name: `File ${index}` }))))
    const onChange = vi.fn()
    render(<ProjectInputPicker chatId="chat" workspaceId="w" userId="h1" selected={Array.from({ length: 16 }, (_, i) => `version${i}`)} onChange={onChange} refreshKey={0} />)
    const boxes = await screen.findAllByRole("checkbox")
    expect(boxes[16]).toBeDisabled()
    expect(boxes[0]).not.toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }))
    expect(onChange).toHaveBeenLastCalledWith([])
  })
})
