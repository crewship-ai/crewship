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
// The trigger's name is "Model: unknown" until run-preflight answers.
const open = async (name: RegExp = /Model: (?!unknown)/) => {
  const trigger = await screen.findByRole("button", { name })
  fireEvent.keyDown(trigger, { key: "Enter" })
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
  route(() => json(preflight()), () => json({ detail: "boom" }, 502))
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
  await screen.findAllByRole("menuitem")
  expect(screen.getAllByRole("menuitem").every((el) => el.getAttribute("aria-disabled") === "true" || el.hasAttribute("data-disabled"))).toBe(true)
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
