import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

import {
  parseProposalSuggestion,
  createOnboardingProposal,
  applyOnboardingProposal,
  startSetupAgentSession,
  resolveOnboardingWorkspaceId,
  loadOnboardingResumeState,
  updateOnboardingWorkspace,
  validateWorkspaceModelCredential,
  createWorkspaceModelCredential,
} from "../setup-agent-api"

// apiFetch wraps the global fetch with auth-refresh plumbing this file has no
// business exercising — mock the module boundary, not the network.
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

import { apiFetch } from "@/lib/api-fetch"

const mockedApiFetch = vi.mocked(apiFetch)

beforeEach(() => {
  mockedApiFetch.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response
}

describe("resolveOnboardingWorkspaceId", () => {
  it("chooses the oldest workspace even though the API returns newest first", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, [
      { id: "ws_new", created_at: "2026-08-22T12:00:00Z" },
      { id: "ws_old", created_at: "2026-08-20T12:00:00Z" },
    ]))

    await expect(resolveOnboardingWorkspaceId()).resolves.toBe("ws_old")
  })

  it("preserves server order when legacy rows have no timestamps", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, [
      { id: "ws_first" },
      { id: "ws_second" },
    ]))

    await expect(resolveOnboardingWorkspaceId()).resolves.toBe("ws_first")
  })
})

describe("onboarding credential persistence", () => {
  it("rejects an Anthropic account API key before the CLI-token probe", async () => {
    await expect(validateWorkspaceModelCredential({
      provider: "ANTHROPIC",
      value: "sk-ant-api03-example",
    })).resolves.toMatchObject({ ok: false, error: expect.stringContaining("claude setup-token") })
    expect(mockedApiFetch).not.toHaveBeenCalled()
  })

  it("uses the OAuth probe only for an Anthropic OAuth token", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, { valid: true, status: 200, supported: true }))
    await validateWorkspaceModelCredential({ provider: "ANTHROPIC", value: "sk-ant-oat01-example" })
    expect(JSON.parse(String((mockedApiFetch.mock.calls[0][1] as RequestInit).body))).toMatchObject({
      type: "AI_CLI_TOKEN",
    })
  })

  it("returns the provider's rejection instead of persisting a dead token", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, {
      valid: false,
      status: 401,
      supported: true,
      error: "Invalid API key",
    }))
    await expect(validateWorkspaceModelCredential({ provider: "OPENAI", value: "bad-token" }))
      .resolves.toEqual({ ok: false, error: "Invalid API key" })
  })

  it("does not strand an Anthropic setup token on an inconclusive probe", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, {
      valid: false,
      status: 0,
      supported: true,
      error: "Could not reach Anthropic",
    }))
    await expect(validateWorkspaceModelCredential({
      provider: "ANTHROPIC",
      value: "sk-ant-oat01-example",
    })).resolves.toEqual({ ok: true })
  })

  it("updates the matching row after a wizard refresh instead of creating a duplicate", async () => {
    mockedApiFetch
      .mockResolvedValueOnce(jsonResponse(200, [
        { id: "cred_existing", name: "ANTHROPIC_API_KEY", provider: "ANTHROPIC", status: "ACTIVE" },
      ]))
      .mockResolvedValueOnce(jsonResponse(200, {}))

    await expect(createWorkspaceModelCredential({
      workspaceId: "ws_1",
      name: "ANTHROPIC_API_KEY",
      provider: "ANTHROPIC",
      value: "sk-ant-oat01-new",
    })).resolves.toEqual({ ok: true, credentialId: "cred_existing" })
    expect(mockedApiFetch).toHaveBeenCalledTimes(2)
    expect(mockedApiFetch.mock.calls[1][0]).toBe("/api/v1/credentials/cred_existing?workspace_id=ws_1")
    expect(mockedApiFetch.mock.calls[1][1]).toMatchObject({ method: "PATCH" })
  })

  it("creates a new credential when no matching row exists", async () => {
    mockedApiFetch
      .mockResolvedValueOnce(jsonResponse(200, []))
      .mockResolvedValueOnce(jsonResponse(201, { id: "cred_new" }))

    await expect(createWorkspaceModelCredential({
      workspaceId: "ws_1",
      name: "OPENAI_API_KEY",
      provider: "OPENAI",
      value: "sk-example",
    })).resolves.toEqual({ ok: true, credentialId: "cred_new" })
    expect(mockedApiFetch.mock.calls[1][1]).toMatchObject({ method: "POST" })
    expect(JSON.parse(String((mockedApiFetch.mock.calls[1][1] as RequestInit).body))).toMatchObject({
      type: "API_KEY",
      provider: "OPENAI",
    })
  })
})

describe("onboarding resume", () => {
  it("restores the oldest workspace and an active model credential without returning its secret", async () => {
    mockedApiFetch
      .mockResolvedValueOnce(jsonResponse(200, [
        { id: "ws_new", name: "New", created_at: "2026-08-22T12:00:00Z" },
        {
          id: "ws_old",
          name: "Pavel's Workspace",
          preferred_language: "Czech",
          created_at: "2026-08-20T12:00:00Z",
        },
      ]))
      .mockResolvedValueOnce(jsonResponse(200, [
        {
          id: "cred_1",
          name: "ANTHROPIC_API_KEY",
          provider: "ANTHROPIC",
          type: "AI_CLI_TOKEN",
          status: "ACTIVE",
        },
      ]))

    await expect(loadOnboardingResumeState()).resolves.toEqual({
      ok: true,
      state: {
        workspaceId: "ws_old",
        workspaceName: "Pavel's Workspace",
        preferredLanguage: "Czech",
        savedCredential: {
          id: "cred_1",
          name: "ANTHROPIC_API_KEY",
          provider: "ANTHROPIC",
        },
      },
    })
    expect(mockedApiFetch.mock.calls[1][0]).toBe("/api/v1/credentials?workspace_id=ws_old")
  })

  it("fails closed when credential state cannot be restored", async () => {
    mockedApiFetch
      .mockResolvedValueOnce(jsonResponse(200, [{ id: "ws_1", name: "Acme" }]))
      .mockResolvedValueOnce(jsonResponse(500, { error: "database unavailable" }))

    await expect(loadOnboardingResumeState()).resolves.toEqual({
      ok: false,
      error: "database unavailable",
    })
  })

  it("persists workspace choices before leaving the first step", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, {}))
    await expect(updateOnboardingWorkspace({
      workspaceId: "ws_1",
      name: " Acme ",
      preferredLanguage: "Czech",
    })).resolves.toEqual({ ok: true })

    expect(mockedApiFetch).toHaveBeenCalledWith(
      "/api/v1/workspaces/ws_1",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ name: "Acme", preferred_language: "Czech" }),
      }),
    )
  })
})

// A realistic Create/Get response, shaped exactly like
// internal/api.onboardingProposalResponse / onboardingProposalPayload —
// cross-checked against cmd/crewship/cmd_onboarding.go's mirrored structs.
const WIRE_PROPOSAL = {
  id: "prop_1",
  workspace_id: "ws_1",
  created_by: "user_1",
  created_at: "2026-08-22T00:00:00Z",
  applied_at: null,
  status: "PENDING",
  payload: {
    crew_name: "Seznam Listing Scraper",
    crew_slug: "seznam-listing-scraper",
    template_slug: "software-development",
    llm_provider: "ANTHROPIC",
    llm_model: "claude-sonnet-5",
    agents: [
      {
        name: "Tech Lead",
        slug: "tech-lead-seznam-listing-scraper",
        role_title: "Architect",
        llm_provider: "ANTHROPIC",
        llm_model: "claude-sonnet-5",
        system_prompt: "You are...",
      },
      {
        name: "Backend Dev",
        slug: "backend-dev-seznam-listing-scraper",
        role_title: "Engineer",
        llm_provider: "ANTHROPIC",
        llm_model: "claude-sonnet-5",
        system_prompt: "You are...",
      },
    ],
  },
}

describe("parseProposalSuggestion — reads the agent's suggestion, not a finished proposal", () => {
  it("returns null when metadata carries no suggestion", () => {
    expect(parseProposalSuggestion(undefined)).toBeNull()
    expect(parseProposalSuggestion({})).toBeNull()
    expect(parseProposalSuggestion({ onboarding_proposal_suggestion: null })).toBeNull()
    expect(parseProposalSuggestion({ onboarding_proposal_suggestion: "nope" })).toBeNull()
  })

  it("requires both a crew name and a template slug", () => {
    expect(
      parseProposalSuggestion({ onboarding_proposal_suggestion: { crew_name: "X" } }),
    ).toBeNull()
    expect(
      parseProposalSuggestion({ onboarding_proposal_suggestion: { template_slug: "software-development" } }),
    ).toBeNull()
  })

  it("parses a well-formed suggestion, optional fields included", () => {
    expect(
      parseProposalSuggestion({
        onboarding_proposal_suggestion: {
          crew_name: "Seznam Listing Scraper",
          template_slug: "software-development",
          llm_model: "claude-sonnet-5",
        },
      }),
    ).toEqual({
      crewName: "Seznam Listing Scraper",
      templateSlug: "software-development",
      crewSlug: undefined,
      llmProvider: undefined,
      llmModel: "claude-sonnet-5",
    })
  })

  it("accepts a suggestion with agents but no template slug (a bespoke crew)", () => {
    expect(
      parseProposalSuggestion({
        onboarding_proposal_suggestion: {
          crew_name: "Web Monitoring Crew",
          agents: [{ name: "Monitoring Engineer", role: "Watches uptime" }],
        },
      }),
    ).toEqual({
      crewName: "Web Monitoring Crew",
      templateSlug: undefined,
      crewSlug: undefined,
      llmProvider: undefined,
      llmModel: undefined,
      agents: [{ name: "Monitoring Engineer", role: "Watches uptime" }],
    })
  })

  it("requires a crew name even when agents are present", () => {
    expect(
      parseProposalSuggestion({
        onboarding_proposal_suggestion: { agents: [{ name: "A", role: "R" }] },
      }),
    ).toBeNull()
  })

  it("drops malformed agent entries but keeps the well-formed ones", () => {
    expect(
      parseProposalSuggestion({
        onboarding_proposal_suggestion: {
          crew_name: "X",
          template_slug: "software-development",
          agents: [{ name: "Good", role: "Fine" }, { name: "" }, "nope", { role: "no name" }],
        },
      }),
    ).toEqual(
      expect.objectContaining({
        agents: [{ name: "Good", role: "Fine" }],
      }),
    )
  })
})

describe("createOnboardingProposal — the non-sensitive half (a preview, not a crew)", () => {
  it("POSTs the suggestion's fields under their wire names", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(201, WIRE_PROPOSAL))
    await createOnboardingProposal({
      crewName: "Seznam Listing Scraper",
      templateSlug: "software-development",
      llmModel: "claude-sonnet-5",
    }, "ws 1")
    expect(mockedApiFetch).toHaveBeenCalledWith(
      "/api/v1/onboarding/proposals?workspace_id=ws%201",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          crew_name: "Seznam Listing Scraper",
          template_slug: "software-development",
          llm_model: "claude-sonnet-5",
        }),
      }),
    )
  })

  it("POSTs a custom agents roster, and template_slug is omitted when the suggestion had none", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(201, WIRE_PROPOSAL))
    await createOnboardingProposal({
      crewName: "Web Monitoring Crew",
      templateSlug: undefined,
      agents: [{ name: "Monitoring Engineer", role: "Watches uptime" }],
    }, "ws_1")
    expect(mockedApiFetch).toHaveBeenCalledWith(
      "/api/v1/onboarding/proposals?workspace_id=ws_1",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          crew_name: "Web Monitoring Crew",
          agents: [{ name: "Monitoring Engineer", role: "Watches uptime" }],
        }),
      }),
    )
  })

  it("maps every agent's role_title/llm_model to the card's role/model fields, dropping nothing", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(201, WIRE_PROPOSAL))
    const proposal = await createOnboardingProposal({
      crewName: "X",
      templateSlug: "software-development",
    }, "ws_1")
    expect(proposal.id).toBe("prop_1")
    expect(proposal.crewName).toBe("Seznam Listing Scraper")
    expect(proposal.agents).toEqual([
      { name: "Tech Lead", role: "Architect", model: "claude-sonnet-5" },
      { name: "Backend Dev", role: "Engineer", model: "claude-sonnet-5" },
    ])
  })

  it("Phase 1 carries no egress data on the wire — always an empty list, never fabricated", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(201, WIRE_PROPOSAL))
    const proposal = await createOnboardingProposal({ crewName: "X", templateSlug: "software-development" }, "ws_1")
    expect(proposal.egressDomains).toEqual([])
  })

  it("prefers the RFC 7807 detail member on failure", async () => {
    mockedApiFetch.mockResolvedValueOnce(
      jsonResponse(404, { type: "about:blank", title: "Not Found", status: 404, detail: "Template not found" }),
    )
    await expect(
      createOnboardingProposal({ crewName: "X", templateSlug: "nonexistent" }, "ws_1"),
    ).rejects.toThrow("Template not found")
  })

  it("falls back to the legacy {error} member when detail is absent", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(400, { error: "crew_name is required" }))
    await expect(
      createOnboardingProposal({ crewName: "", templateSlug: "software-development" }, "ws_1"),
    ).rejects.toThrow("crew_name is required")
  })

  it("throws on a malformed 2xx body rather than returning a broken proposal", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(201, { not: "a proposal" }))
    await expect(
      createOnboardingProposal({ crewName: "X", templateSlug: "software-development" }, "ws_1"),
    ).rejects.toThrow(/Malformed/)
  })
})

describe("applyOnboardingProposal — the only write a proposal card may trigger", () => {
  it("POSTs to the proposal's own apply URL and sends no body", async () => {
    mockedApiFetch.mockResolvedValueOnce(
      jsonResponse(200, {
        proposal_id: "prop_1",
        status: "APPLIED",
        already_applied: false,
        crew: { crew_id: "crew_1", crew_name: "Seznam Listing Scraper", crew_slug: "seznam-listing-scraper", agent_count: 2, agent_ids: ["a1", "a2"] },
      }),
    )
    const result = await applyOnboardingProposal("prop_1", "ws 1")
    expect(mockedApiFetch).toHaveBeenCalledTimes(1)
    const [url, init] = mockedApiFetch.mock.calls[0]
    expect(url).toBe("/api/v1/onboarding/proposals/prop_1/apply?workspace_id=ws%201")
    expect(init).toMatchObject({ method: "POST" })
    // No body at all — the handler "deliberately never parses a request
    // body" (PRD §5.6); nothing here may give it one to parse.
    expect((init as RequestInit | undefined)?.body).toBeUndefined()
    expect(result).toEqual({
      proposalId: "prop_1",
      alreadyApplied: false,
      crewId: "crew_1",
      crewName: "Seznam Listing Scraper",
      crewSlug: "seznam-listing-scraper",
      agentCount: 2,
      agentIds: ["a1", "a2"],
    })
  })

  it("URL-encodes the proposal id", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, {}))
    await applyOnboardingProposal("weird id/with slash", "ws 1")
    expect(mockedApiFetch).toHaveBeenCalledWith(
      "/api/v1/onboarding/proposals/weird%20id%2Fwith%20slash/apply?workspace_id=ws%201",
      expect.anything(),
    )
  })

  it("surfaces already_applied so a duplicate click can be told it was a no-op replay", async () => {
    mockedApiFetch.mockResolvedValueOnce(
      jsonResponse(200, {
        proposal_id: "prop_1",
        status: "APPLIED",
        already_applied: true,
        crew: { crew_id: "crew_1", crew_name: "X", crew_slug: "x", agent_count: 1, agent_ids: ["a1"] },
      }),
    )
    const result = await applyOnboardingProposal("prop_1", "ws_1")
    expect(result.alreadyApplied).toBe(true)
  })

  it("throws the RFC 7807 detail on failure", async () => {
    mockedApiFetch.mockResolvedValueOnce(
      jsonResponse(409, { type: "about:blank", title: "Conflict", status: 409, detail: "slug already exists" }),
    )
    await expect(applyOnboardingProposal("prop_1", "ws_1")).rejects.toThrow("slug already exists")
  })

  it("falls back to a generic message when the error body is unreadable", async () => {
    mockedApiFetch.mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => {
        throw new Error("not json")
      },
    } as unknown as Response)
    await expect(applyOnboardingProposal("prop_1", "ws_1")).rejects.toThrow(/HTTP 500/)
  })
})

describe("startSetupAgentSession — real endpoint, never throws", () => {
  it("POSTs to the real endpoint", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, { agent_id: "a1", session_id: "s1", workspace_id: "ws_1" }))
    await startSetupAgentSession()
    expect(mockedApiFetch).toHaveBeenCalledWith(
      "/api/v1/onboarding/setup-agent/start",
      expect.objectContaining({ method: "POST" }),
    )
  })

  it("returns the session on a well-formed 200", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, { agent_id: "a1", session_id: "s1", workspace_id: "ws_1" }))
    await expect(startSetupAgentSession()).resolves.toEqual({
      ok: true,
      session: { agentId: "a1", sessionId: "s1", workspaceId: "ws_1" },
    })
  })

  it('distinguishes the "no credential yet" precondition (428) from a real failure', async () => {
    mockedApiFetch.mockResolvedValueOnce(
      jsonResponse(428, { error: "Add a model token first.", reason: "credential_required" }),
    )
    await expect(startSetupAgentSession()).resolves.toEqual({ ok: false, reason: "credential_required" })
  })

  // The fallback the caller relies on for a REAL failure (not the expected
  // "no credential yet" precondition above) — task requirement: a genuine
  // failure must still resolve to the "unavailable" fallback the UI falls
  // back to the template grid on, never an unhandled rejection.
  it("falls back to reason=unavailable on a genuine server failure (500)", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(500, { error: "boom" }))
    await expect(startSetupAgentSession()).resolves.toEqual({ ok: false, reason: "unavailable" })
  })

  it("falls back to reason=unavailable on a 404", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(404, {}))
    await expect(startSetupAgentSession()).resolves.toEqual({ ok: false, reason: "unavailable" })
  })

  it("falls back to reason=unavailable on a malformed 200 body rather than throwing", async () => {
    mockedApiFetch.mockResolvedValueOnce(jsonResponse(200, { agent_id: "a1" }))
    await expect(startSetupAgentSession()).resolves.toEqual({ ok: false, reason: "unavailable" })
  })

  it("falls back to reason=unavailable on a network failure rather than throwing", async () => {
    mockedApiFetch.mockRejectedValueOnce(new Error("network down"))
    await expect(startSetupAgentSession()).resolves.toEqual({ ok: false, reason: "unavailable" })
  })
})
