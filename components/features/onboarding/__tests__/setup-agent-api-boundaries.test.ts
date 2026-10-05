import { beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { applyOnboardingProposal, createOnboardingProposal, createWorkspaceModelCredential, loadOnboardingResumeState, parseProposalSuggestion, resolveOnboardingWorkspaceId, startSetupAgentSession, updateOnboardingWorkspace, updateWorkspaceModelCredential, validateWorkspaceModelCredential } from "../setup-agent-api"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetch = vi.mocked(apiFetch)
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
const broken = (status = 200) => new Response("invalid JSON", { status })
const input = { workspaceId: "ws/a", name: "Test credential", provider: "OPENAI", value: "test-fixture-value" }
beforeEach(() => { fetch.mockReset() })

it.each([null, {}, [], [{ id: 9 }], [{ id: "" }]])("does not resolve an invalid workspace response %j", async (body) => {
  fetch.mockResolvedValue(reply(body)); expect(await resolveOnboardingWorkspaceId()).toBeNull()
})
it.each(["network", "http", "json"])("returns null when workspace lookup fails with %s", async (failure) => {
  if (failure === "network") fetch.mockRejectedValue(new Error("offline"))
  else fetch.mockResolvedValue(failure === "http" ? reply({}, 503) : broken())
  expect(await resolveOnboardingWorkspaceId()).toBeNull()
})
it.each(["workspace-http", "workspace-json", "no-workspace", "credentials-http", "network"])("fails resume closed on %s", async (failure) => {
  if (failure === "network") fetch.mockRejectedValue(new Error("offline"))
  else if (failure === "workspace-http") fetch.mockResolvedValue(reply({ detail: "Unauthorized" }, 401))
  else if (failure === "workspace-json") fetch.mockResolvedValue(broken())
  else if (failure === "no-workspace") fetch.mockResolvedValue(reply([]))
  else fetch.mockResolvedValueOnce(reply([{ id: "ws" }])).mockResolvedValueOnce(reply({}, 503))
  expect(await loadOnboardingResumeState()).toMatchObject({ ok: false, error: expect.any(String) })
})
it("ignores invalid, unrelated, revoked and account credentials during resume", async () => {
  fetch.mockResolvedValueOnce(reply([{ id: "ws", name: 5, preferred_language: 0 }])).mockResolvedValueOnce(reply([
    null, 7, { id: "account", provider: "ANTHROPIC", type: "API_KEY", status: "ACTIVE" },
    { id: "revoked", provider: "ANTHROPIC", type: "AI_CLI_TOKEN", status: "REVOKED" },
    { id: "other", provider: "OPENAI", type: "API_KEY" }, { id: "valid", provider: "anthropic", type: "ai_cli_token" },
  ]))
  expect(await loadOnboardingResumeState()).toEqual({ ok: true, state: { workspaceId: "ws", workspaceName: "", preferredLanguage: null, savedCredential: { id: "valid", provider: "ANTHROPIC", name: "Model credential" } } })
})
it.each([null, {}, [null, 3, { provider: "ANTHROPIC", type: "AI_CLI_TOKEN", id: 7 }]])("resumes without inventing a credential from %j", async (body) => {
  fetch.mockResolvedValueOnce(reply([{ id: "ws" }])).mockResolvedValueOnce(reply(body))
  expect(await loadOnboardingResumeState()).toMatchObject({ ok: true, state: { savedCredential: null } })
})
it.each(["workspace", "credential"])("encodes %s update identifiers and preserves errors", async (kind) => {
  fetch.mockResolvedValue(reply({ detail: "Access denied" }, 403))
  const result = kind === "workspace" ? await updateOnboardingWorkspace({ workspaceId: "ws/a", name: " A ", preferredLanguage: "Czech" }) : await updateWorkspaceModelCredential({ workspaceId: "ws/a", credentialId: "id/b", value: "replacement" })
  expect(result).toEqual({ ok: false, error: "Access denied" })
  expect(fetch.mock.calls[0][0]).toBe(kind === "workspace" ? "/api/v1/workspaces/ws%2Fa" : "/api/v1/credentials/id%2Fb?workspace_id=ws%2Fa")
})
it.each(["workspace", "credential", "validation", "create"])("turns %s transport errors into recoverable outcomes", async (kind) => {
  fetch.mockRejectedValue(new Error("offline"))
  const outcome = kind === "workspace" ? await updateOnboardingWorkspace({ workspaceId: "ws", name: "Example", preferredLanguage: "English" }) : kind === "credential" ? await updateWorkspaceModelCredential({ workspaceId: "ws", credentialId: "id", value: "replacement" }) : kind === "validation" ? await validateWorkspaceModelCredential(input) : await createWorkspaceModelCredential(input)
  expect(outcome).toEqual({ ok: false, error: expect.stringContaining("Couldn't reach") })
})
it.each([null, 7, {}, { supported: false }, { supported: true, valid: false }, { supported: true, valid: false, error: "" }])("rejects unsupported or malformed provider validation %j", async (body) => {
  fetch.mockResolvedValue(reply(body)); expect(await validateWorkspaceModelCredential(input)).toMatchObject({ ok: false, error: expect.any(String) })
})
it.each(["http", "json"])("reports validation %s errors without persisting", async (kind) => {
  fetch.mockResolvedValue(kind === "http" ? reply({}, 503) : broken())
  expect(await validateWorkspaceModelCredential(input)).toMatchObject({ ok: false })
  expect(fetch).toHaveBeenCalledTimes(1)
})
it("never creates an Anthropic account key through CLI onboarding", async () => {
  expect(await createWorkspaceModelCredential({ ...input, provider: "ANTHROPIC" })).toMatchObject({ ok: false })
  expect(fetch).not.toHaveBeenCalled()
})
it.each(["network", "http", "json", "shape"])("still attempts creation when the optional credential lookup fails with %s", async (kind) => {
  if (kind === "network") fetch.mockRejectedValueOnce(new Error("offline"))
  else fetch.mockResolvedValueOnce(kind === "http" ? reply({}, 500) : kind === "json" ? broken() : reply({ items: [] }))
  fetch.mockResolvedValueOnce(reply({ id: "new" }))
  expect(await createWorkspaceModelCredential(input)).toEqual({ ok: true, credentialId: "new" })
  expect(fetch.mock.calls[1][1]?.method).toBe("POST")
  expect(JSON.parse(String(fetch.mock.calls[1][1]?.body))).toMatchObject({ type: "API_KEY", scope: "WORKSPACE" })
})
it.each([null, {}, { id: "" }, { id: 4 }])("rejects a malformed credential creation response %j", async (body) => {
  fetch.mockResolvedValueOnce(reply([null, 9, { name: input.name, provider: input.provider, status: "REVOKED", id: "old" }, { name: input.name, provider: input.provider, id: 3 }])).mockResolvedValueOnce(reply(body))
  expect(await createWorkspaceModelCredential(input)).toEqual({ ok: false, error: "Malformed credential response" })
})
it("surfaces the canonical credential POST error when no reusable row exists", async () => {
  fetch.mockResolvedValueOnce(reply([])).mockResolvedValueOnce(reply({ error: "Name taken" }, 409))
  expect(await createWorkspaceModelCredential(input)).toEqual({ ok: false, error: "Name taken" })
})
it.each([null, 5, { agent_id: "a" }, { agent_id: "a", session_id: "s", workspace_id: 4 }])("rejects incomplete setup-agent session %j", async (body) => {
  fetch.mockResolvedValue(reply(body)); expect(await startSetupAgentSession()).toEqual({ ok: false, reason: "unavailable" })
})
it("validates optional suggestion fields and drops empty or malformed agents and tools", () => {
  expect(parseProposalSuggestion({ onboarding_proposal_suggestion: { crew_name: "Crew", template_slug: "blank", crew_slug: 3, llm_provider: 3, llm_model: "", crew_icon: "", crew_color: 4, agents: [null, 3, { name: "" }, { name: "A", role: "" }], tools: ["git", "", null, 3] } })).toMatchObject({ crewName: "Crew", templateSlug: "blank", agents: undefined, tools: ["git"] })
})
it("renders server proposal defaults without fabricating agents or network access", async () => {
  fetch.mockResolvedValue(reply({ id: "proposal", payload: { agents: [null, {}, { name: "One" }, { name: "Two", role_title: "", llm_model: "" }], tools: ["git", 7], crew_icon: "globe", crew_color: "blue" } }))
  const proposal = await createOnboardingProposal({ crewName: "Crew", templateSlug: "blank" }, "ws")
  expect(proposal).toMatchObject({ crewName: "New crew", crewSlug: "", templateSlug: "", status: "PENDING", tools: ["git"], egressDomains: [], crewIcon: "globe", crewColor: "blue", agents: [{ name: "One", role: "Agent", model: "unspecified" }, { name: "Two", role: "Agent", model: "unspecified" }] })
})
it.each([null, 8, { id: "" }, { id: 3 }])("rejects unidentifiable proposal %j", async (body) => {
  fetch.mockResolvedValue(reply(body)); await expect(createOnboardingProposal({ crewName: "Crew", templateSlug: "blank" }, "ws")).rejects.toThrow()
})
it("accepts an identifiable proposal without optional payload", async () => {
  fetch.mockResolvedValue(reply({ id: "p" })); expect(await createOnboardingProposal({ crewName: "Crew", templateSlug: "blank" }, "ws")).toMatchObject({ id: "p", agents: [], tools: [] })
})
it.each([null, 4, { crew: 3 }, { proposal_id: 3, already_applied: "yes", crew: { crew_id: 2, crew_slug: false, crew_name: null, agent_count: "3" } }])("does not fabricate apply acknowledgement fields from %j", async (body) => {
  fetch.mockResolvedValue(reply(body)); const result = await applyOnboardingProposal("p", "ws")
  expect(result.crewId).toBeUndefined(); expect(result.agentIds).toBeUndefined(); expect(result.alreadyApplied).toBeUndefined()
})
