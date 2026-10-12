import { act, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { SharedContextTab } from "../shared-context-tab"

const fetchAPI = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchAPI }))

const baseAgent = {
  name: "Researcher", slug: "researcher", agent_role: "SPECIALIST",
  description: null, system_prompt: null, tool_profile: null,
  cli_adapter: null, llm_provider: null, llm_model: null, crew_id: null,
}
const response = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

beforeEach(() => { fetchAPI.mockReset() })
afterEach(() => { vi.restoreAllMocks() })

describe("SharedContextTab", () => {
  it("waits for a workspace and renders minimal agent context without a crew request", async () => {
    fetchAPI.mockResolvedValue(response(baseAgent))
    const { rerender } = render(<SharedContextTab agentId="a1" workspaceId={null} />)
    expect(screen.getByText("Select a workspace to view context.")).toBeInTheDocument()
    expect(fetchAPI).not.toHaveBeenCalled()
    rerender(<SharedContextTab agentId="a1" workspaceId="w1" />)
    expect(await screen.findByText("Researcher")).toBeInTheDocument()
    expect(screen.getByText("SPECIALIST")).toBeInTheDocument()
    expect(screen.queryByText("System Prompt")).not.toBeInTheDocument()
    expect(screen.queryByText("Crew")).not.toBeInTheDocument()
    expect(fetchAPI).toHaveBeenCalledTimes(1)
  })

  it("keeps loading until crew context arrives and renders the configured boundaries", async () => {
    const crew = deferred<ReturnType<typeof response>>()
    fetchAPI.mockResolvedValueOnce(response({ ...baseAgent, crew_id: "c1", description: "Collect evidence", system_prompt: "Cite original sources", tool_profile: "read-only", cli_adapter: "claude-code", llm_provider: "ANTHROPIC", llm_model: "custom-model" }))
      .mockReturnValueOnce(crew.promise)
    render(<SharedContextTab agentId="a1" workspaceId="w1" />)
    await waitFor(() => expect(fetchAPI).toHaveBeenCalledTimes(2))
    expect(screen.queryByText("Researcher")).not.toBeInTheDocument()
    expect(fetchAPI.mock.calls[1][0]).toBe("/api/v1/crews/c1?workspace_id=w1")
    expect(fetchAPI.mock.calls[0][1].signal).toBe(fetchAPI.mock.calls[1][1].signal)
    await act(async () => { crew.resolve(response({ name: "Analysis", description: "Independent research", network_mode: "restricted", allowed_domains: "example.org" })) })
    expect(await screen.findByText("Analysis")).toBeInTheDocument()
    for (const text of ["Collect evidence", "Cite original sources", "read-only", "Independent research", "Network: restricted (example.org)"]) {
      expect(screen.getByText(text)).toBeInTheDocument()
    }
    expect(screen.getByText(/Anthropic.*custom-model/)).toBeInTheDocument()
  })

  it.each([
    { llm_provider: "ANTHROPIC", llm_model: null, expected: /Anthropic.*default/ },
    { llm_provider: null, llm_model: "custom-model", expected: /—.*custom-model/ },
  ])("shows model/provider fallbacks: $llm_provider $llm_model", async ({ expected, ...model }) => {
    fetchAPI.mockResolvedValue(response({ ...baseAgent, ...model, cli_adapter: "custom-adapter" }))
    render(<SharedContextTab agentId="a1" workspaceId="w1" />)
    expect(await screen.findByText(expected)).toBeInTheDocument()
    expect(screen.getByText("custom-adapter")).toBeInTheDocument()
  })

  it.each([null, "restricted"])("renders crew without optional description/domains: %s", async (network_mode) => {
    fetchAPI.mockResolvedValueOnce(response({ ...baseAgent, crew_id: "c1" }))
      .mockResolvedValueOnce(response({ name: "Minimal crew", description: null, network_mode, allowed_domains: null }))
    render(<SharedContextTab agentId="a1" workspaceId="w1" />)
    expect(await screen.findByText("Minimal crew")).toBeInTheDocument()
    if (network_mode) expect(screen.getByText("Network: restricted")).toBeInTheDocument()
    else expect(screen.queryByText(/Network:/)).not.toBeInTheDocument()
  })

  it.each(["agent", "crew"])("reports unavailable %s without displaying partial context", async (failure) => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    fetchAPI.mockResolvedValueOnce(response(failure === "agent" ? {} : { ...baseAgent, crew_id: "c1" }, failure === "agent" ? 403 : 200))
      .mockResolvedValueOnce(response({}, 404))
    render(<SharedContextTab agentId="a1" workspaceId="w1" />)
    expect(await screen.findByText("Unable to load agent")).toBeInTheDocument()
    expect(screen.queryByText("Researcher")).not.toBeInTheDocument()
  })

  it("does not let a late crew response replace context after a workspace change", async () => {
    const oldCrew = deferred<ReturnType<typeof response>>()
    fetchAPI.mockResolvedValueOnce(response({ ...baseAgent, crew_id: "c1" }))
      .mockReturnValueOnce(oldCrew.promise)
      .mockResolvedValueOnce(response({ ...baseAgent, name: "New researcher" }))
    const { rerender } = render(<SharedContextTab agentId="a1" workspaceId="w1" />)
    await waitFor(() => expect(fetchAPI).toHaveBeenCalledTimes(2))
    const oldSignal = fetchAPI.mock.calls[1][1].signal as AbortSignal
    rerender(<SharedContextTab agentId="a2" workspaceId="w2" />)
    expect(await screen.findByText("New researcher")).toBeInTheDocument()
    expect(oldSignal.aborted).toBe(true)
    await act(async () => { oldCrew.resolve(response({ name: "Old crew" })) })
    expect(screen.queryByText("Old crew")).not.toBeInTheDocument()
    expect(screen.getByText("New researcher")).toBeInTheDocument()
    rerender(<SharedContextTab agentId="a2" workspaceId={null} />)
    expect(screen.queryByText("New researcher")).not.toBeInTheDocument()
  })

  it("encodes all resource and workspace identifiers in chained requests", async () => {
    fetchAPI.mockResolvedValueOnce(response({ ...baseAgent, crew_id: "crew/a?b" }))
      .mockResolvedValueOnce(response({ name: "Encoded crew" }))
    render(<SharedContextTab agentId="agent/a?b" workspaceId="w&scope=other" />)
    await screen.findByText("Encoded crew")
    expect(fetchAPI.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/agents/agent%2Fa%3Fb?workspace_id=w%26scope%3Dother",
      "/api/v1/crews/crew%2Fa%3Fb?workspace_id=w%26scope%3Dother",
    ])
  })
})
