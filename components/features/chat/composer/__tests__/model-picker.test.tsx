import React from "react"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { useComposerStore } from "@/stores/composer-store"
import { ModelPicker } from "../model-picker"

const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))

const preflight = (over: Record<string, unknown> = {}) => ({
  scope: "configured_chat_agent", agent_id: "a", model: "claude-sonnet-4-6",
  payer: { state: "ready", category: "connected_provider_account", label: "Workspace payer", provider: "ANTHROPIC" },
  budgets: [], gate: { configured_mode: "sync", predicted_mode: "none", rule_matches: false, blocking: false, reason: "" },
  estimate_usd: null, estimate_state: "unknown", constraints: [], ...over,
})
const catalogue = (source: string, ids: string[]) => ({
  provider: "ANTHROPIC", source, models: ids.map((id) => ({ id, display_name: `Name ${id}`, provider: "ANTHROPIC" })),
})
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

function route(pf: () => Response, models: () => Response | Promise<Response>) {
  fetch.mockImplementation((url: string) => String(url).includes("run-preflight") ? Promise.resolve(pf()) : Promise.resolve(models()))
}
function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(<QueryClientProvider client={client}><ModelPicker chatId="chat" workspaceId="ws" userId="u" /></QueryClientProvider>)
}
// jsdom cannot turn Enter into a click; the real keyboard path is in the e2e.
// The trigger's name is "Model: unknown" until run-preflight answers.
const open = async (name: RegExp = /Model: (?!unknown)/) => {
  const trigger = await screen.findByRole("button", { name })
  trigger.focus()
  fireEvent.click(trigger)
}
const modelCalls = () => fetch.mock.calls.map(([u]) => String(u)).filter((u) => !u.includes("run-preflight"))

beforeEach(() => { useComposerStore.setState({ modelId: null }) })
afterEach(() => { cleanup(); fetch.mockReset() })

it("never calls the dead /llm/models path and asks /models for the configured provider", async () => {
  route(() => json(preflight()), () => json(catalogue("curated", ["claude-sonnet-4-6"])))
  setup()
  await open()
  await screen.findByText(/Curated list/)
  expect(modelCalls()).toEqual(["/api/v1/models?provider=ANTHROPIC&workspace_id=ws"])
  expect(fetch.mock.calls.some(([u]) => String(u).includes("/llm/models"))).toBe(false)
})

it("shows the configured model from run-preflight as current and does not pick models[0]", async () => {
  route(() => json(preflight({ model: "claude-sonnet-4-6" })), () => json(catalogue("live", ["aaa-first", "claude-sonnet-4-6"])))
  setup()
  expect(await screen.findByRole("button", { name: /Model: claude-sonnet-4-6/ })).toBeInTheDocument()
  await open()
  await screen.findByText(/Live list/)
  expect(screen.getByRole("button", { name: /Model: claude-sonnet-4-6/, hidden: true })).toBeInTheDocument()
  expect(useComposerStore.getState().modelId).toBeNull()
})

it("keeps the configured model and says so when the catalogue fails", async () => {
  route(() => json(preflight()), () => json({ detail: "boom" }, 500))
  setup()
  await open()
  expect(await screen.findByText(/Could not load the model list/)).toBeInTheDocument()
  expect(screen.getByText(/still uses claude-sonnet-4-6/)).toBeInTheDocument()
  expect(screen.getByRole("button", { name: /Model: claude-sonnet-4-6/, hidden: true })).toBeInTheDocument()
  expect(useComposerStore.getState().modelId).toBeNull()
})

it("explains an empty catalogue", async () => {
  route(() => json(preflight()), () => json(catalogue("curated", [])))
  setup()
  await open()
  expect(await screen.findByText(/No models are listed for ANTHROPIC/)).toBeInTheDocument()
})

it("shows a loading state while the catalogue is pending", async () => {
  route(() => json(preflight()), () => new Promise<Response>(() => {}))
  setup()
  await open()
  expect(await screen.findByText(/Loading models/)).toBeInTheDocument()
})

it("does not claim the listed models are permitted or in effect", async () => {
  route(() => json(preflight()), () => json(catalogue("curated", ["claude-sonnet-4-6", "other"])))
  setup()
  await open()
  expect(await screen.findByText(/does not mean you may use it/)).toBeInTheDocument()
  expect(await screen.findAllByRole("listitem")).toHaveLength(2)
  expect(screen.queryAllByRole("menu")).toHaveLength(0)
  expect(screen.queryAllByRole("menuitem")).toHaveLength(0)
  expect(screen.getByText(/not a permission check/)).toBeInTheDocument()
})

it("flags a configured model that the catalogue does not list", async () => {
  route(() => json(preflight({ model: "custom-x" })), () => json(catalogue("curated", ["claude-sonnet-4-6"])))
  setup()
  await open()
  expect(await screen.findByText(/not in this list/)).toBeInTheDocument()
})

it("does not guess a provider when the server reports none", async () => {
  route(() => json(preflight({ payer: { state: "unknown", category: "none", label: "" } })), () => json(catalogue("curated", ["x"])))
  setup()
  await open()
  expect(await screen.findByText(/provider is not reported/)).toBeInTheDocument()
  expect(modelCalls()).toEqual([])
})

it("renders nothing when the chat is not readable", async () => {
  route(() => json({ error: "no" }, 404), () => json(catalogue("curated", [])))
  const { container } = setup()
  await waitFor(() => expect(fetch).toHaveBeenCalled())
  await waitFor(() => expect(container).toBeEmptyDOMElement())
})

it("makes no /models request until the menu opens", async () => {
  route(() => json(preflight()), () => json(catalogue("curated", ["claude-sonnet-4-6"])))
  setup()
  await screen.findByRole("button", { name: /Model: claude-sonnet-4-6/ })
  expect(modelCalls()).toEqual([])
  await open()
  await screen.findByText(/Curated list/)
  expect(modelCalls()).toHaveLength(1)
})

it("says no list exists for a provider the server cannot list (400)", async () => {
  route(() => json(preflight()), () => json({ detail: "unsupported provider" }, 400))
  setup()
  await open()
  expect(await screen.findByText("No model list is available for this provider.")).toBeInTheDocument()
  expect(screen.queryByText(/Could not load/)).not.toBeInTheDocument()
})

it("says the provider is unreachable (502)", async () => {
  route(() => json(preflight()), () => json({ detail: "down" }, 502))
  setup()
  await open()
  expect(await screen.findByText(/Provider unreachable\./)).toBeInTheDocument()
  expect(screen.getByText(/still uses claude-sonnet-4-6/)).toBeInTheDocument()
})

it("distinguishes an agent with no model set from a preflight failure", async () => {
  route(() => json(preflight({ model: "" })), () => json(catalogue("curated", ["x"])))
  setup()
  await open(/Model: not set/)
  expect(await screen.findByText("No model is set on the agent; the provider default may apply.")).toBeInTheDocument()
  expect(screen.queryByText(/could not be read/)).not.toBeInTheDocument()
})

it("says the configured model could not be read when preflight fails", async () => {
  route(() => json({ error: "x" }, 503), () => json(catalogue("curated", ["x"])))
  setup()
  await open(/Model: unknown/)
  expect(await screen.findByText("The configured model could not be read.")).toBeInTheDocument()
})

it("offers a focusable, labelled, scrollable list and marks the configured row in text", async () => {
  route(() => json(preflight()), () => json(catalogue("curated", ["claude-sonnet-4-6", "other"])))
  setup()
  await open()
  const list = await screen.findByRole("list", { name: "Models listed for ANTHROPIC" })
  expect(list).toHaveAttribute("tabindex", "0")
  expect(list.className).toContain("overflow-y-auto")
  list.focus()
  expect(list).toHaveFocus()
  expect(screen.getAllByRole("listitem")[0]).toHaveTextContent("Configured for this chat")
  expect(screen.getAllByRole("listitem")[1]).not.toHaveTextContent("Configured for this chat")
})

it("closes on Escape and returns focus to the trigger", async () => {
  route(() => json(preflight()), () => json(catalogue("curated", ["claude-sonnet-4-6"])))
  setup()
  await open()
  const list = await screen.findByRole("list", { name: /Models listed/ })
  list.focus()
  fireEvent.keyDown(list, { key: "Escape" })
  await waitFor(() => expect(screen.queryByRole("list", { name: /Models listed/ })).not.toBeInTheDocument())
  expect(screen.getByRole("button", { name: /Model: claude-sonnet-4-6/ })).toHaveFocus()
})

it("keeps status and notes readable inside the panel", async () => {
  route(() => json(preflight()), () => json({ detail: "x" }, 502))
  setup()
  await open()
  const status = await screen.findByText(/Provider unreachable/)
  expect(status.closest('[role="dialog"]')).not.toBeNull()
})
