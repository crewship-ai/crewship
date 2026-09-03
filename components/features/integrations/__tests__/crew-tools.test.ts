import { describe, it, expect, vi, beforeEach } from "vitest"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { bindCredentialToCrewTool, needsCredential, sortCrewTools, type CrewTool } from "@/components/features/integrations/crew-tools"

const tool = (id: string, crew: string, auth_status: string, name = id): CrewTool => ({
  id, crew_id: `c-${crew}`, crew_name: crew, crew_slug: crew.toLowerCase(), name, display_name: name,
  transport: "streamable-http", endpoint: "https://mcp.example/x", command: null, enabled: true, agent_binding_count: 0, auth_status,
})

describe("needsCredential / sortCrewTools", () => {
  it("counts missing and expired as gaps, and lists them first", () => {
    const sorted = sortCrewTools([tool("a", "Ops", "connected"), tool("b", "Engineering", "missing"), tool("c", "Quality", "expired"), tool("d", "Engineering", "none")])
    expect(sorted.map((t) => t.id)).toEqual(["c", "b", "d", "a"])
    expect(sorted.map(needsCredential)).toEqual([true, true, false, false])
  })
})

describe("bindCredentialToCrewTool", () => {
  const calls: { url: string; init?: RequestInit }[] = []
  beforeEach(() => {
    calls.length = 0
    vi.mocked(apiFetch).mockImplementation(async (url: string, init?: RequestInit) => {
      calls.push({ url, init })
      const json = (body: unknown, ok = true, status = 200) => ({ ok, status, json: async () => body }) as unknown as Response
      if (url.startsWith("/api/v1/agents?")) return json([{ id: "alex" }, { id: "sam" }])
      if (init?.method === "POST" && url.startsWith("/api/v1/agents/sam/integrations?")) return json({ id: "b2" }, false, 500)
      if (init?.method) return json({})
      if (url === "/api/v1/agents/alex/integrations?workspace_id=ws") return json([{ id: "b1", mcp_server_id: "srv", mcp_server_scope: "crew" }])
      if (url === "/api/v1/agents/sam/integrations?workspace_id=ws") return json([])
      return json({})
    })
  })

  it("patches an existing binding and creates the missing one, reporting failures by agent", async () => {
    const result = await bindCredentialToCrewTool({ workspaceId: "ws", tool: { id: "srv", crew_id: "c-eng" }, credentialId: "cred_1" })
    expect(result).toEqual({ bound: 1, failures: ["agent sam: HTTP 500"] })

    const patch = calls.find((c) => c.init?.method === "PATCH")
    expect(patch?.url).toBe("/api/v1/agents/alex/integrations/b1?workspace_id=ws")
    expect(JSON.parse(String(patch?.init?.body))).toEqual({ credential_id: "cred_1", cred_type: "bearer" })

    const post = calls.find((c) => c.init?.method === "POST")
    expect(post?.url).toBe("/api/v1/agents/sam/integrations?workspace_id=ws")
    expect(JSON.parse(String(post?.init?.body))).toMatchObject({ mcp_server_id: "srv", mcp_server_scope: "crew", credential_id: "cred_1", enabled: true })
  })

  it("asks for the crew's agents, not the whole workspace", async () => {
    await bindCredentialToCrewTool({ workspaceId: "ws", tool: { id: "srv", crew_id: "c-eng" }, credentialId: "cred_1" })
    expect(calls[0].url).toBe("/api/v1/agents?workspace_id=ws&crew_id=c-eng&limit=500")
  })
})
