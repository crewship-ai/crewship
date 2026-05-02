import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { submitCrew } from "../submit"
import { INITIAL_STATE, type WizardState } from "../types"

// submit.ts now fires toast.warning on PATCH override failure. Stub sonner so
// tests don't crash on missing toaster context.
vi.mock("sonner", () => ({
  toast: { warning: vi.fn(), error: vi.fn(), success: vi.fn(), info: vi.fn() },
}))

// =============================================================================
// fetch helper — single source of truth for what each call returned, so a
// failed assertion can show "deploy returned 500, then patch returned 404".
// =============================================================================

interface MockCall {
  url: string
  method: string
  body: Record<string, unknown> | undefined
}

function setupFetchMock() {
  const calls: MockCall[] = []
  const responses: Array<{ ok: boolean; status: number; body: unknown }> = []

  const fetchMock = vi.fn(async (url: string | URL, init?: RequestInit) => {
    const u = typeof url === "string" ? url : url.toString()
    let parsedBody: Record<string, unknown> | undefined
    if (init?.body && typeof init.body === "string") {
      try { parsedBody = JSON.parse(init.body) } catch { /* leave undefined */ }
    }
    calls.push({ url: u, method: init?.method ?? "GET", body: parsedBody })
    const r = responses.shift() ?? { ok: true, status: 200, body: {} }
    return {
      ok: r.ok,
      status: r.status,
      json: async () => r.body,
      text: async () => (typeof r.body === "string" ? r.body : JSON.stringify(r.body)),
    } as Response
  })

  vi.stubGlobal("fetch", fetchMock)
  return {
    calls,
    queueResponse: (r: { ok: boolean; status?: number; body: unknown }) => {
      responses.push({ ok: r.ok, status: r.status ?? 200, body: r.body })
    },
  }
}

const WS = "ws_123"

function fullState(overrides: Partial<WizardState> = {}): WizardState {
  return {
    ...INITIAL_STATE,
    name: "Engineering",
    slug: "engineering",
    description: "Backend services",
    icon: "code",
    color: "blue",
    memoryMB: 2048,
    cpus: 1,
    ttlHours: 4,
    ...overrides,
  }
}

describe("submitCrew — blank mode", () => {
  let fetcher: ReturnType<typeof setupFetchMock>

  beforeEach(() => {
    fetcher = setupFetchMock()
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it("POSTs to /api/v1/crews with full identity + runtime body", async () => {
    fetcher.queueResponse({
      ok: true,
      body: { id: "crew_1", slug: "engineering", name: "Engineering" },
    })

    const result = await submitCrew(WS, fullState({ mode: "empty" }))

    expect(result).toMatchObject({ id: "crew_1", slug: "engineering", name: "Engineering" })
    expect(fetcher.calls).toHaveLength(1)

    const call = fetcher.calls[0]
    expect(call.method).toBe("POST")
    expect(call.url).toContain("/api/v1/crews")
    // wsCtx middleware (RequireWorkspace) reads workspace_id from the query
    // string and rejects 400 otherwise — so this MUST be present on every
    // crew API call.
    expect(call.url).toContain(`workspace_id=${WS}`)

    expect(call.body).toMatchObject({
      name: "Engineering",
      slug: "engineering",
      icon: "code",
      color: "blue",
      description: "Backend services",
      container_memory_mb: 2048,
      container_cpus: 1,
      container_ttl_hours: 4,
      network_mode: "free",
    })
  })

  it("omits description when blank (don't write empty string to DB)", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({ mode: "empty", description: "   " }))

    expect(fetcher.calls[0].body).not.toHaveProperty("description")
  })

  it("omits container_ttl_hours when ttl is null (Never)", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({ mode: "empty", ttlHours: null }))

    expect(fetcher.calls[0].body).not.toHaveProperty("container_ttl_hours")
  })

  it("omits allowed_domains when network_mode is free", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({
      mode: "empty",
      networkMode: "free",
      allowedDomains: ["github.com"], // shouldn't leak when mode is free
    }))

    expect(fetcher.calls[0].body).not.toHaveProperty("allowed_domains")
  })

  it("includes allowed_domains when network_mode is restricted", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({
      mode: "empty",
      networkMode: "restricted",
      allowedDomains: ["github.com", "*.npmjs.org"],
    }))

    expect(fetcher.calls[0].body).toMatchObject({
      network_mode: "restricted",
      allowed_domains: ["github.com", "*.npmjs.org"],
    })
  })

  it("trims whitespace from name / slug / description", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({
      mode: "empty",
      name: "  Padded Name  ",
      slug: "  padded  ",
      description: "  some desc  ",
    }))

    expect(fetcher.calls[0].body).toMatchObject({
      name: "Padded Name",
      slug: "padded",
      description: "some desc",
    })
  })

  it("throws with backend error text on non-2xx", async () => {
    fetcher.queueResponse({ ok: false, status: 409, body: "slug already exists" })

    await expect(submitCrew(WS, fullState({ mode: "empty" }))).rejects.toThrow(/slug already exists/)
  })
})

describe("submitCrew — browse (template) mode", () => {
  let fetcher: ReturnType<typeof setupFetchMock>

  beforeEach(() => {
    fetcher = setupFetchMock()
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it("deploys the template, then PATCHes the resulting crew with overrides", async () => {
    fetcher.queueResponse({
      ok: true,
      body: { crew_id: "crew_42", crew_name: "Engineering", crew_slug: "engineering" },
    })
    fetcher.queueResponse({ ok: true, body: {} }) // PATCH

    const result = await submitCrew(WS, fullState({
      mode: "browse",
      pickedTemplateSlug: "software-development",
    }))

    expect(result).toMatchObject({ id: "crew_42", slug: "engineering", name: "Engineering" })
    expect(fetcher.calls).toHaveLength(2)

    // Call 1: POST /api/v1/crew-templates/{slug}/deploy
    expect(fetcher.calls[0].url).toContain("/api/v1/crew-templates/software-development/deploy")
    expect(fetcher.calls[0].method).toBe("POST")
    expect(fetcher.calls[0].body).toEqual({
      crew_name: "Engineering",
      crew_slug: "engineering",
    })

    // Call 2: PATCH /api/v1/crews/{id} with identity + runtime overrides
    expect(fetcher.calls[1].url).toContain("/api/v1/crews/crew_42")
    expect(fetcher.calls[1].method).toBe("PATCH")
    expect(fetcher.calls[1].body).toMatchObject({
      icon: "code",
      color: "blue",
      description: "Backend services",
      container_memory_mb: 2048,
      container_cpus: 1,
      container_ttl_hours: 4,
      network_mode: "free",
    })
  })

  it("URL-encodes the template slug to defend against weird chars", async () => {
    fetcher.queueResponse({
      ok: true,
      body: { crew_id: "x", crew_name: "X", crew_slug: "x" },
    })
    fetcher.queueResponse({ ok: true, body: {} })

    await submitCrew(WS, fullState({
      mode: "browse",
      pickedTemplateSlug: "ops/team",
    }))

    expect(fetcher.calls[0].url).toContain("ops%2Fteam/deploy")
  })

  it("rejects with helpful error when no template was picked", async () => {
    await expect(submitCrew(WS, fullState({
      mode: "browse",
      pickedTemplateSlug: null,
    }))).rejects.toThrow(/No template selected/)
  })

  it("propagates deploy failure (template not found, slug conflict, etc.)", async () => {
    fetcher.queueResponse({ ok: false, status: 404, body: "Template not found" })

    await expect(submitCrew(WS, fullState({
      mode: "browse",
      pickedTemplateSlug: "nonexistent",
    }))).rejects.toThrow(/Template not found/)
  })

  it("does NOT throw if PATCH override fails — crew exists with template defaults", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    fetcher.queueResponse({
      ok: true,
      body: { crew_id: "crew_42", crew_name: "X", crew_slug: "x" },
    })
    fetcher.queueResponse({ ok: false, status: 500, body: "boom" })

    const result = await submitCrew(WS, fullState({
      mode: "browse",
      pickedTemplateSlug: "any",
    }))

    expect(result.id).toBe("crew_42")
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })
})

// =============================================================================
// Container fields (Step 4)
// =============================================================================

describe("submitCrew — container fields (image, devcontainer, mise, MCP)", () => {
  let fetcher: ReturnType<typeof setupFetchMock>
  beforeEach(() => { fetcher = setupFetchMock() })
  afterEach(() => { vi.unstubAllGlobals() })

  it("blank mode: passes runtime_image / devcontainer_config / mise_config on initial POST", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({
      mode: "empty",
      runtimeImage: "ubuntu:22.04",
      devcontainerConfig: '{"image":"ubuntu:22.04","features":{"ghcr.io/devcontainers/features/git:1":{}}}',
      miseConfig: '[tools]\npython = "3.12"',
    }))

    expect(fetcher.calls[0].body).toMatchObject({
      runtime_image: "ubuntu:22.04",
      devcontainer_config: '{"image":"ubuntu:22.04","features":{"ghcr.io/devcontainers/features/git:1":{}}}',
      mise_config: '[tools]\npython = "3.12"',
    })
  })

  it("blank mode: omits container fields when blank (don't send empty strings)", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({ mode: "empty" }))

    expect(fetcher.calls[0].body).not.toHaveProperty("runtime_image")
    expect(fetcher.calls[0].body).not.toHaveProperty("devcontainer_config")
    expect(fetcher.calls[0].body).not.toHaveProperty("mise_config")
  })

  it("blank mode: PATCHes mcp_config_json after POST when set (POST doesn't accept it)", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "crew_1", slug: "x", name: "X" } })
    fetcher.queueResponse({ ok: true, body: {} })

    await submitCrew(WS, fullState({
      mode: "empty",
      mcpConfig: '{"mcpServers":{"github":{"command":"npx","args":["@modelcontextprotocol/server-github"]}}}',
    }))

    expect(fetcher.calls).toHaveLength(2)
    expect(fetcher.calls[0].method).toBe("POST")
    expect(fetcher.calls[0].body).not.toHaveProperty("mcp_config_json")
    expect(fetcher.calls[1].method).toBe("PATCH")
    expect(fetcher.calls[1].url).toContain("/api/v1/crews/crew_1")
    expect(fetcher.calls[1].body).toMatchObject({
      mcp_config_json: '{"mcpServers":{"github":{"command":"npx","args":["@modelcontextprotocol/server-github"]}}}',
    })
  })

  it("blank mode: skips PATCH entirely when no MCP config set", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({ mode: "empty" }))

    expect(fetcher.calls).toHaveLength(1)
    expect(fetcher.calls[0].method).toBe("POST")
  })

  it("browse mode: PATCH override includes container fields + MCP", async () => {
    fetcher.queueResponse({ ok: true, body: { crew_id: "crew_1", crew_name: "X", crew_slug: "x" } })
    fetcher.queueResponse({ ok: true, body: {} })

    await submitCrew(WS, fullState({
      mode: "browse",
      pickedTemplateSlug: "any",
      runtimeImage: "alpine:3.19",
      devcontainerConfig: '{"image":"alpine:3.19","features":{}}',
      miseConfig: '[tools]\nnode = "20"',
      mcpConfig: '{"mcpServers":{"slack":{"type":"http","url":"https://example/sse"}}}',
    }))

    expect(fetcher.calls[1].method).toBe("PATCH")
    expect(fetcher.calls[1].body).toMatchObject({
      runtime_image: "alpine:3.19",
      devcontainer_config: '{"image":"alpine:3.19","features":{}}',
      mise_config: '[tools]\nnode = "20"',
      mcp_config_json: '{"mcpServers":{"slack":{"type":"http","url":"https://example/sse"}}}',
    })
  })

  it("browse mode: omits container fields from PATCH when blank", async () => {
    fetcher.queueResponse({ ok: true, body: { crew_id: "crew_1", crew_name: "X", crew_slug: "x" } })
    fetcher.queueResponse({ ok: true, body: {} })

    await submitCrew(WS, fullState({ mode: "browse", pickedTemplateSlug: "any" }))

    const patchBody = fetcher.calls[1].body
    expect(patchBody).not.toHaveProperty("runtime_image")
    expect(patchBody).not.toHaveProperty("devcontainer_config")
    expect(patchBody).not.toHaveProperty("mise_config")
    expect(patchBody).not.toHaveProperty("mcp_config_json")
  })
})

describe("submitCrew — dispatcher", () => {
  let fetcher: ReturnType<typeof setupFetchMock>

  beforeEach(() => {
    fetcher = setupFetchMock()
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it("routes 'browse' to template flow (deploy + patch = 2 calls)", async () => {
    fetcher.queueResponse({ ok: true, body: { crew_id: "x", crew_name: "X", crew_slug: "x" } })
    fetcher.queueResponse({ ok: true, body: {} })

    await submitCrew(WS, fullState({ mode: "browse", pickedTemplateSlug: "any" }))

    expect(fetcher.calls).toHaveLength(2)
    expect(fetcher.calls[0].url).toContain("/crew-templates/")
  })

  it("routes 'empty' to blank-create (1 call)", async () => {
    fetcher.queueResponse({ ok: true, body: { id: "x", slug: "x", name: "X" } })

    await submitCrew(WS, fullState({ mode: "empty" }))

    expect(fetcher.calls).toHaveLength(1)
    expect(fetcher.calls[0].url).toContain("/api/v1/crews")
    expect(fetcher.calls[0].url).not.toContain("/crew-templates/")
  })
})
