import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ChatSearchDialog } from "../search/chat-search-dialog"

const api = vi.hoisted(() => vi.fn())
const push = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }))

const roster = [{ id: "a1", slug: "ada", name: "Ada" }]
const hit = { id: "m1", session_id: "s1", agent_id: "a1", agent_slug: "ada", agent_name: "Ada", role: "assistant", content: "deploy <img src=x onerror=alert(1)> done", ts: "2026-10-10T12:00:00Z" }
const setup = (props = {}) => render(<ChatSearchDialog open onOpenChange={() => {}} workspaceId="ws" roster={roster} current={{ agentId: "a1", sessionId: "s1" }} {...props} />)
const type = (value: string) => fireEvent.change(screen.getByLabelText("Search query"), { target: { value } })

beforeEach(() => { api.mockReset(); push.mockReset() })
afterEach(cleanup)

it("renders a hostile snippet as text and opens the exact message with the keyboard", async () => {
  api.mockResolvedValue(Response.json({ hits: [hit] }))
  const { container } = setup()
  type("deploy")
  await screen.findByRole("option")
  expect(container.ownerDocument.querySelector("img")).toBeNull()
  fireEvent.keyDown(screen.getByLabelText("Search query"), { key: "Enter" })
  expect(push).toHaveBeenCalledWith("/chat/ada?session=s1&message=m1")
})

it("shows a server failure as an error with retry, never as no matches", async () => {
  api.mockResolvedValueOnce(new Response("{}", { status: 500 })).mockResolvedValueOnce(Response.json({ hits: [hit] }))
  setup()
  type("deploy")
  expect(await screen.findByRole("alert")).toHaveTextContent("Search failed (500)")
  expect(screen.queryByText("No matches.")).toBeNull()
  fireEvent.click(screen.getByText("Retry"))
  await screen.findByRole("option")
})

it("marks a locally filtered full page as partial", async () => {
  api.mockResolvedValue(Response.json({ hits: Array.from({ length: 50 }, (_, i) => ({ ...hit, id: `m${i}` })) }))
  setup()
  type("deploy has:code")
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("best matches only"))
  expect(screen.getByText("No matches among the results checked.")).toBeInTheDocument()
})

it("reports unsupported filters and unavailable Files / room scopes honestly", async () => {
  setup({ current: null })
  type("deploy has:video")
  expect(screen.getByRole("alert")).toHaveTextContent("has:video")
  fireEvent.click(screen.getByRole("tab", { name: "Files" }))
  expect(screen.getByRole("status")).toHaveTextContent("File search is not available yet.")
  fireEvent.click(screen.getByRole("tab", { name: "This chat" }))
  type("deploy")
  expect(await screen.findByText(/not available for this conversation/)).toBeInTheDocument()
  expect(api).not.toHaveBeenCalled()
})

it("does not open or show a previous scope's rows while the new search is pending", async () => {
  api.mockResolvedValueOnce(Response.json({ hits: [hit] }))
  setup()
  type("deploy")
  await screen.findByRole("option")
  let release: (r: Response) => void = () => {}
  api.mockReturnValueOnce(new Promise<Response>((resolve) => { release = resolve }))
  fireEvent.click(screen.getByRole("tab", { name: "This chat" }))
  expect(screen.queryByRole("option")).toBeNull()
  fireEvent.keyDown(screen.getByLabelText("Search query"), { key: "Enter" })
  expect(push).not.toHaveBeenCalled()
  release(Response.json({ hits: [] }))
})

it("exposes the highlighted result to assistive tech and supports arrow keys between tabs", async () => {
  api.mockResolvedValue(Response.json({ hits: [hit, { ...hit, id: "m2" }] }))
  setup()
  type("deploy")
  const options = await screen.findAllByRole("option")
  const input = screen.getByLabelText("Search query")
  expect(input).toHaveAttribute("aria-activedescendant", options[0].id)
  fireEvent.keyDown(input, { key: "ArrowDown" })
  expect(input).toHaveAttribute("aria-activedescendant", options[1].id)
  fireEvent.keyDown(screen.getByRole("tab", { name: "All chats" }), { key: "ArrowLeft" })
  expect(screen.getByRole("tab", { name: "This chat" })).toHaveAttribute("aria-selected", "true")
  expect(screen.getByRole("tabpanel")).toBeInTheDocument()
})

it("closes on Escape", () => {
  const onOpenChange = vi.fn()
  setup({ onOpenChange })
  fireEvent.keyDown(screen.getByLabelText("Search query"), { key: "Escape" })
  expect(onOpenChange).toHaveBeenCalledWith(false)
})
