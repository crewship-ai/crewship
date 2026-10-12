import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { PersonaDraft } from "@/components/features/crews/persona-draft"
import { apiFetch } from "@/lib/api-fetch"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
beforeEach(() => { vi.mocked(apiFetch).mockReset() })
afterEach(() => { cleanup(); vi.restoreAllMocks() })
const persona = (content: string, from_default = false) => Response.json({ content, from_default, layer: "crew" })

it("distinguishes an untouched inherited persona, an edited draft and removing the override", async () => {
  vi.mocked(apiFetch).mockResolvedValue(persona("Crew style"))
  const onChange = vi.fn()
  const { rerender } = render(<PersonaDraft workspaceId="ws1" agentId="agent1" value={undefined} onChange={onChange} />)
  expect(screen.getByText("Loading persona…")).toBeVisible()
  expect(await screen.findByRole("textbox", { name: "Persona override" })).toHaveValue("Crew style")
  expect(screen.getByText(/Current source: crew/)).toBeVisible()
  expect(screen.queryByText("Draft · saved with the agent")).not.toBeInTheDocument()
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "Draft style" } })
  expect(onChange).toHaveBeenLastCalledWith("Draft style")
  rerender(<PersonaDraft workspaceId="ws1" agentId="agent1" value="Draft style" onChange={onChange} />)
  expect(screen.getByRole("textbox")).toHaveValue("Draft style")
  expect(screen.getByText("Draft · saved with the agent")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Use inherited persona" }))
  expect(onChange).toHaveBeenLastCalledWith(null)
  rerender(<PersonaDraft workspaceId="ws1" agentId="agent1" value={null} onChange={onChange} />)
  expect(screen.getByRole("textbox")).toHaveValue("")
  expect(screen.getByText("The agent override will be removed on Save.")).toBeVisible()
})
it("identifies the default source and preserves an explicitly empty override", async () => {
  vi.mocked(apiFetch).mockResolvedValue(persona("Default style", true))
  render(<PersonaDraft workspaceId="ws1" agentId="agent1" value="" onChange={vi.fn()} />)
  expect(await screen.findByRole("textbox")).toHaveValue("")
  expect(screen.getByText(/Current source: default/)).toBeVisible()
})
it.each(["http", "transport", "body"])("does not present an editable persona when loading fails at %s", async (failure) => {
  if (failure === "transport") vi.mocked(apiFetch).mockRejectedValue(new Error("offline"))
  else vi.mocked(apiFetch).mockResolvedValue(new Response("unreadable", { status: failure === "http" ? 503 : 200 }))
  render(<PersonaDraft workspaceId="ws1" agentId="agent1" value={undefined} onChange={vi.fn()} />)
  expect(await screen.findByRole("alert")).toHaveTextContent("Persona could not be loaded.")
  expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
})
it("cannot restore a previous workspace's persona from a delayed response", async () => {
  let resolveOld!: (response: Response) => void
  vi.mocked(apiFetch).mockReturnValueOnce(new Promise<Response>(resolve => { resolveOld = resolve })).mockResolvedValueOnce(persona("New workspace style"))
  const { rerender } = render(<PersonaDraft workspaceId="old" agentId="agent1" value={undefined} onChange={vi.fn()} />)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledOnce())
  const oldSignal = vi.mocked(apiFetch).mock.calls[0][1]?.signal
  rerender(<PersonaDraft workspaceId="new" agentId="agent1" value={undefined} onChange={vi.fn()} />)
  expect(oldSignal?.aborted).toBe(true)
  expect(await screen.findByRole("textbox")).toHaveValue("New workspace style")
  await act(async () => { resolveOld(persona("Old workspace style")) })
  expect(screen.getByRole("textbox")).toHaveValue("New workspace style")
})
