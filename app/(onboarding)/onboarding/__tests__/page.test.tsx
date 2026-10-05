import type { ComponentProps } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { OnboardingProposal } from "@/components/features/onboarding/setup-agent-api"
import type { OnboardingSetupChat } from "@/components/features/onboarding/onboarding-setup-chat"
import type { OnboardingCreatedPanel } from "@/components/features/onboarding/onboarding-created-panel"
import type { ToolchainPicker } from "@/components/features/onboarding/toolchain-picker"

const mocks = vi.hoisted(() => ({
  api: vi.fn(), server: vi.fn(), resume: vi.fn(), resolve: vi.fn(), workspace: vi.fn(), validate: vi.fn(), create: vi.fn(), update: vi.fn(),
  router: { push: vi.fn(), replace: vi.fn() }, reducedMotion: false,
}))
vi.mock("motion/react", async (original) => ({ ...await original<typeof import("motion/react")>(), useReducedMotion: () => mocks.reducedMotion }))
vi.mock("next/navigation", () => ({ useRouter: () => mocks.router }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: mocks.api }))
vi.mock("@/lib/server-base", () => ({ serverFetch: mocks.server }))
vi.mock("@/components/features/onboarding/setup-agent-api", () => ({
  loadOnboardingResumeState: mocks.resume, resolveOnboardingWorkspaceId: mocks.resolve,
  updateOnboardingWorkspace: mocks.workspace, validateWorkspaceModelCredential: mocks.validate,
  createWorkspaceModelCredential: mocks.create, updateWorkspaceModelCredential: mocks.update,
}))
vi.mock("@/components/features/onboarding/onboarding-preview", async (original) => ({
  ...await original<typeof import("@/components/features/onboarding/onboarding-preview")>(), OnboardingPreview: () => <div>Workspace preview</div>,
}))
vi.mock("@/components/features/onboarding/toolchain-picker", () => ({ ToolchainPicker: ({ onChange }: ComponentProps<typeof ToolchainPicker>) => <div><button onClick={() => onChange("CODEX")}>Select Codex</button><button onClick={() => onChange("CLAUDE_CODE")}>Select Claude</button></div> }))
vi.mock("@/components/features/onboarding/onboarding-created-panel", () => ({ OnboardingCreatedPanel: ({ onCrewsFound }: ComponentProps<typeof OnboardingCreatedPanel>) => <button onClick={() => onCrewsFound?.(2)}>Restore existing crews</button> }))
vi.mock("@/components/features/onboarding/onboarding-proposal-summary", () => ({ OnboardingProposalSummary: ({ created }: { created?: boolean }) => <div>{created ? "Created proposal" : "Prepared proposal"}</div> }))
vi.mock("@/components/features/onboarding/onboarding-setup-chat", () => ({ OnboardingSetupChat: (p: ComponentProps<typeof OnboardingSetupChat>) => <section aria-label="Guide">
  <button onClick={() => p.onUnavailable?.("unavailable")}>Guide offline</button>
  <button onClick={() => p.onUnavailable?.("credential_required")}>Guide needs credential</button>
  <button onClick={() => p.onProposalPrepared?.(proposal)}>Prepare proposal</button>
  <button onClick={() => p.onProposalApplied?.({ crewName: "Renamed crew" }, proposal)}>Apply proposal</button>
  <button onClick={() => p.onProposalApplied?.({}, { ...proposal, id: "p2", crewName: "Second crew" })}>Apply second proposal</button>
</section> }))
import Page from "../page"
const proposal: OnboardingProposal = { id: "p1", crewName: "First crew", crewSlug: "first", templateSlug: "software-development", agents: [{ name: "Ada", role: "Developer", model: "sonnet" }], tools: [], egressDomains: [], status: "prepared", crewIcon: "code", crewColor: "blue" }
const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const snapshot = { workspaceId: "ws-1", workspaceName: "Example", preferredLanguage: null as string | null, savedCredential: null as { id: string; provider: string } | null }
function resume(step: 1 | 2 | 3) {
  mocks.resume.mockResolvedValue({ ok: true, state: { ...snapshot, preferredLanguage: step > 1 ? "en" : null, savedCredential: step === 3 ? { id: "saved", provider: "anthropic" } : null } })
}
async function open(step: 1 | 2 | 3 = 1) {
  resume(step); render(<Page />)
  await screen.findByRole("button", { name: step === 3 ? "Launch" : "Continue" })
}
async function template() { await open(3); fireEvent.click(screen.getByRole("button", { name: /Prefer to pick/ })); fireEvent.click(screen.getByRole("button", { name: /Software Development/ })) }
function token(value = "fixture-token-value") { fireEvent.change(screen.getByLabelText("Claude Code CLI token"), { target: { value } }) }
function click(name: string) { fireEvent.click(screen.getByRole("button", { name })) }
function posted(url: string) { const call = mocks.api.mock.calls.find(([path]) => path === url); expect(call).toBeDefined(); return JSON.parse(call![1].body) }
beforeEach(() => {
  vi.resetAllMocks(); mocks.reducedMotion = false
  const storage = new Map<string, string>()
  vi.mocked(localStorage.getItem).mockImplementation((key) => storage.get(key) ?? null)
  vi.mocked(localStorage.setItem).mockImplementation((key, value) => { storage.set(key, value) })
  vi.mocked(localStorage.removeItem).mockImplementation((key) => { storage.delete(key) })
  mocks.api.mockImplementation(async (path: string) => reply(path.endsWith("/status") ? { completed: false } : {}))
  mocks.server.mockImplementation(async (path: string) => reply(path.endsWith("/runtime") ? { available: true, in_use: true } : { enabled: false }))
  mocks.resolve.mockResolvedValue("ws-1"); mocks.workspace.mockResolvedValue({ ok: true }); mocks.validate.mockResolvedValue({ ok: true })
  mocks.create.mockResolvedValue({ ok: true, credentialId: "new-credential" }); mocks.update.mockResolvedValue({ ok: true, credentialId: "saved" })
  resume(1)
})
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

it("persists workspace before credentials, reuses unchanged tokens and patches replacements", async () => {
  await open(); fireEvent.change(screen.getByPlaceholderText("e.g. Acme Engineering"), { target: { value: "a" } })
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  fireEvent.change(screen.getByPlaceholderText("e.g. Acme Engineering"), { target: { value: "Acme" } }); click("Continue")
  await screen.findByLabelText("Claude Code CLI token")
  expect(mocks.workspace).toHaveBeenCalledWith({ workspaceId: "ws-1", name: "Acme", preferredLanguage: "English (US)" })
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  token(); click("Show token"); expect(screen.getByLabelText("Claude Code CLI token")).toHaveAttribute("type", "text"); click("Hide token")
  click("Continue"); await screen.findByRole("region", { name: "Guide" })
  expect(mocks.validate).toHaveBeenCalledWith({ provider: "ANTHROPIC", value: "fixture-token-value" })
  expect(mocks.create).toHaveBeenCalledOnce()
  click("Back"); await screen.findByLabelText("Claude Code CLI token"); click("Continue"); await screen.findByRole("region", { name: "Guide" })
  expect(mocks.create).toHaveBeenCalledOnce(); expect(mocks.validate).toHaveBeenCalledOnce()
  click("Back"); await screen.findByLabelText("Claude Code CLI token"); token("replacement-token"); click("Continue")
  await screen.findByRole("region", { name: "Guide" })
  expect(mocks.update).toHaveBeenCalledWith({ workspaceId: "ws-1", credentialId: "new-credential", value: "replacement-token" })
})
it.each([401, 500])("fails closed on status HTTP %i, then retries", async (status) => {
  mocks.api.mockResolvedValueOnce(reply({}, status)); render(<Page />)
  expect(await screen.findByText(`Could not verify onboarding status (HTTP ${status}).`)).toBeVisible()
  expect(mocks.resume).not.toHaveBeenCalled(); click("Retry"); await screen.findByPlaceholderText("e.g. Acme Engineering")
})
it("redirects completed onboarding without loading credentials", async () => {
  mocks.api.mockResolvedValueOnce(reply({ completed: true })); render(<Page />)
  await waitFor(() => expect(mocks.router.replace).toHaveBeenCalledWith("/")); expect(mocks.resume).not.toHaveBeenCalled()
})
it.each(["network", "resume"]) ("surfaces %s bootstrap failure", async (kind) => {
  if (kind === "network") mocks.api.mockRejectedValueOnce(new Error("offline"))
  else mocks.resume.mockResolvedValueOnce({ ok: false, error: "Workspace unavailable" })
  render(<Page />); await screen.findByRole("button", { name: "Retry" }); expect(screen.queryByRole("button", { name: "Continue" })).toBeNull()
})
it.each(["validation", "create"]) ("keeps model step editable when %s fails", async (kind) => {
  (kind === "validation" ? mocks.validate : mocks.create).mockResolvedValueOnce({ ok: false, error: "Token rejected" })
  await open(2); token(); click("Continue"); await waitFor(() => expect(screen.getByText("Token rejected")).toBeVisible())
  expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled(); click("Continue"); await screen.findByRole("region", { name: "Guide" })
})
it("does not advance when workspace persistence fails and allows retry", async () => {
  mocks.workspace.mockResolvedValueOnce({ ok: false, error: "Workspace locked" }); await open(); click("Continue")
  await waitFor(() => expect(screen.getByText("Workspace locked")).toBeVisible()); click("Continue"); await screen.findByLabelText("Claude Code CLI token")
})
it.each([false, true])("blocks absent runtime (Docker available=%s) until re-check", async (available) => {
  mocks.server.mockImplementation(async (path: string) => reply(path.endsWith("/runtime") ? { available, in_use: false } : {}))
  await open(2); token(); expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  expect(screen.getByTestId("onboarding-runtime-blocker")).toHaveTextContent(available ? "Restart the server" : "Install or start Docker")
  mocks.server.mockResolvedValue(reply({ available: true, in_use: true })); click("Re-check")
  await waitFor(() => expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled())
})
it("explains experimental adapter and token type gates", async () => {
  await open(2); token("sk-ant-api-test-fixture"); expect(screen.getByTestId("onboarding-token-hint")).toHaveTextContent("account API key")
  token("sk-ant-oat-test-fixture"); expect(screen.getByTestId("onboarding-token-hint")).toHaveTextContent("CLI token")
  click("Select Codex"); expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  expect(screen.getByTestId("onboarding-blocking-reason")).toHaveTextContent("Choose Claude Code")
  click("Select Claude"); expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled()
})
it.each(["Guide offline", "Guide needs credential"])("offers a template after %s", async (name) => {
  await open(3); click(name); await screen.findByRole("heading", { name: "Pick your first crew" })
  expect(screen.getByRole("button", { name: "Launch" })).toBeDisabled()
  const back = screen.queryByRole("button", { name: /Talk to Crewship Guide/ })
  if (name === "Guide offline") expect(back).toBeNull()
  else { await waitFor(() => expect(back).toBeVisible()); fireEvent.click(back!); await screen.findByRole("region", { name: "Guide" }) }
})
it("launches a template with a saved credential and resolves the chat slug", async () => {
  mocks.api.mockImplementation(async (path: string) => reply(path.endsWith("/setup") ? { agent_id: "agent/id", workspace_id: "ws/id" } : { completed: false }))
  mocks.server.mockImplementation(async (path: string) => reply(path.includes("/agents/") ? { slug: "hello/world" } : { available: true, in_use: true }))
  await template(); click("Launch"); await screen.findByRole("heading", { name: "Your crew is ready" })
  expect(posted("/api/v1/onboarding/setup")).toMatchObject({ crew_template_slug: "software-development", credential_value: "" })
  expect(localStorage.getItem("crewship.firstAgentSlug")).toBe("hello/world")
  click("Start chatting"); expect(mocks.router.push).toHaveBeenCalledWith("/chat/hello%2Fworld")
  click("Go to dashboard"); expect(mocks.router.push).toHaveBeenLastCalledWith("/")
})
it("records multiple applied proposals only once and submits the latest id", async () => {
  await open(3); click("Prepare proposal"); await waitFor(() => expect(screen.getByText("Prepared proposal")).toBeVisible())
  click("Apply proposal"); click("Apply proposal"); await waitFor(() => expect(screen.queryByText("Prepared proposal")).toBeNull())
  click("Apply second proposal"); click("Launch")
  await screen.findByRole("heading", { name: "Your 2 crews are ready" })
  expect(posted("/api/v1/onboarding/setup")).toMatchObject({ applied_proposal_id: "p2" })
  await waitFor(() => expect(screen.getByText("Renamed crew")).toBeVisible()); click("Go to dashboard"); expect(mocks.router.push).toHaveBeenCalledWith("/")
})
it.each([200, 409])("completes restored crews without creating another (HTTP %i)", async (status) => {
  mocks.api.mockImplementation(async (path: string) => reply({}, path.endsWith("/complete") ? status : 200))
  localStorage.setItem("crewship.firstAgentSlug", "old"); await open(3); click("Restore existing crews"); click("Launch")
  await screen.findByRole("heading", { name: "Your crew is ready" })
  expect(posted("/api/v1/onboarding/complete")).toEqual({ skipped: false })
  expect(mocks.api.mock.calls.some(([path]) => path.endsWith("/setup"))).toBe(false)
  expect(localStorage.getItem("crewship.firstAgentSlug")).toBeNull()
})
it.each(["setup", "complete"])("preserves retry after %s is rejected", async (endpoint) => {
  mocks.api.mockImplementation(async (path: string) => reply(path.endsWith(`/${endpoint}`) ? { error: "Try later" } : {}, path.endsWith(`/${endpoint}`) ? 503 : 200))
  if (endpoint === "setup") await template(); else { await open(3); click("Restore existing crews") }
  click("Launch"); await waitFor(() => expect(screen.getByText("Try later")).toBeVisible()); expect(screen.getByRole("button", { name: "Launch" })).toBeEnabled()
})
it.each([200, 409, 503])("skip explicitly records the decision and handles HTTP %i", async (status) => {
  mocks.api.mockImplementation(async (path: string) => reply({ error: "Skip unavailable" }, path.endsWith("/complete") ? status : 200))
  await open(); click("Skip setup"); click("Keep going"); expect(mocks.api).toHaveBeenCalledTimes(1)
  click("Skip setup"); click("Skip anyway")
  await waitFor(() => expect(posted("/api/v1/onboarding/complete")).toEqual({ skipped: true }))
  if (status === 503) { await waitFor(() => expect(screen.getByText("Skip unavailable")).toBeVisible()); expect(mocks.router.push).not.toHaveBeenCalled() }
  else await waitFor(() => expect(mocks.router.push).toHaveBeenCalledWith("/"))
})

it.each(["empty", "network", "invalid"])("finishes safely when agent lookup is %s", async (kind) => {
  mocks.api.mockImplementation(async (path: string) => reply(path.endsWith("/setup") ? { agent_id: "a1", workspace_id: "ws1" } : {}))
  mocks.server.mockImplementation(async (path: string) => {
    if (!path.includes("/agents/")) return reply({ available: true, in_use: true })
    if (kind === "network") throw new Error("lookup offline")
    return reply({ slug: 42 }, kind === "empty" ? 404 : 200)
  })
  localStorage.setItem("crewship.firstAgentSlug", "stale")
  await template(); click("Launch"); await screen.findByRole("button", { name: "Go to dashboard" })
  expect(localStorage.getItem("crewship.firstAgentSlug")).toBeNull()
  expect(localStorage.getItem("crewship.firstAgentId")).toBe("a1")
})
it.each(["setup", "skip"])("recovers from a network error during %s", async (action) => {
  mocks.api.mockImplementation(async (path: string) => {
    if (path.endsWith("/status")) return reply({})
    throw new Error("offline")
  })
  if (action === "setup") { await template(); click("Launch") }
  else { await open(); click("Skip setup"); click("Skip anyway") }
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Couldn't reach the server"))
  expect(mocks.router.push).not.toHaveBeenCalled()
  expect(screen.getByRole("button", { name: "Skip setup" })).toBeEnabled()
})
it("redirects when another tab completed setup first", async () => {
  mocks.api.mockImplementation(async (path: string) => reply({}, path.endsWith("/setup") ? 409 : 200))
  await template(); click("Launch"); await waitFor(() => expect(mocks.router.push).toHaveBeenCalledWith("/"))
})
it.each(["workspace", "credential"])("disables navigation while %s is being persisted", async (kind) => {
  let finish!: (v: unknown) => void
  const pending = new Promise((resolve) => { finish = resolve })
  if (kind === "workspace") mocks.workspace.mockReturnValueOnce(pending)
  else mocks.validate.mockReturnValueOnce(pending)
  await open(kind === "workspace" ? 1 : 2); if (kind === "credential") token(); click("Continue")
  expect(screen.getByRole("button", { name: "Back" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Skip setup" })).toBeDisabled()
  await act(async () => finish({ ok: true }))
  if (kind === "workspace") await screen.findByLabelText("Claude Code CLI token")
  else await screen.findByRole("region", { name: "Guide" })
})
it.each(["workspace", "credential"])("requires a resolved workspace before writing %s", async (kind) => {
  mocks.resume.mockResolvedValue({ ok: true, state: { ...snapshot, workspaceId: null, preferredLanguage: kind === "credential" ? "English" : null } })
  mocks.resolve.mockResolvedValue(null); render(<Page />); await screen.findByRole("button", { name: "Continue" })
  if (kind === "credential") token(); click("Continue")
  await waitFor(() => expect(screen.getByText(/Could not find your workspace/)).toBeVisible())
  expect(mocks.workspace).not.toHaveBeenCalled(); expect(mocks.create).not.toHaveBeenCalled()
})
it.each(["cs-CZ", "xx-unknown", ""])("detects browser language %s without blocking setup", async (locale) => {
  vi.spyOn(navigator, "language", "get").mockReturnValue(locale)
  await open(); click("Continue"); await screen.findByLabelText("Claude Code CLI token")
  expect(mocks.workspace).toHaveBeenCalledWith(expect.objectContaining({ preferredLanguage: locale === "cs-CZ" ? "Czech" : "English" }))
})
it("lets the user search and select the agent language", async () => {
  await open(); click("Pick a language")
  fireEvent.change(screen.getByPlaceholderText("Search language…"), { target: { value: "Čeština" } })
  fireEvent.click(await screen.findByRole("option", { name: /Czech/ }))
  expect(screen.getByRole("button", { name: "Pick a language" })).toHaveTextContent("Czech")
  click("Continue"); await screen.findByLabelText("Claude Code CLI token")
  expect(mocks.workspace).toHaveBeenCalledWith(expect.objectContaining({ preferredLanguage: "Czech" }))
})
it.each(["http", "network"])("allows manual retry after pairing %s failure", async (kind) => {
  let attempts = 0
  mocks.server.mockImplementation(async (path: string) => {
    if (!path.endsWith("/pair/start")) return reply({ available: true, in_use: true })
    if (++attempts === 1) { if (kind === "network") throw new Error("offline"); return reply({ error: "Pair denied" }, 503) }
    return reply({ code: "PAIR-FIXTURE", expires_at: new Date(Date.now() + 120000).toISOString() })
  })
  await open(2); click("Also pair my CLI Optional — signs your terminal in too.")
  await screen.findByRole("button", { name: "Retry" }); expect(attempts).toBe(1); click("Retry")
  await screen.findByRole("button", { name: "Copy command" }); expect(attempts).toBe(2)
  expect(screen.getByText(/crewship login --pair --code=PAIR-FIXTURE/)).toHaveTextContent(`--server=${window.location.origin}`)
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
})
it.each(["consumed", "expired"])("observes a pairing code becoming %s", async (status) => {
  mocks.server.mockImplementation(async (path: string) => reply(path.endsWith("/pair/start") ? { code: "PAIR-CODE", expires_at: new Date(Date.now() + 120000).toISOString() } : { available: true, in_use: true }))
  const fetchMock = vi.fn(async (path: string) => reply(path.includes("/poll?") ? { status } : { credentials: [{ provider: "anthropic", status: "ACTIVE" }] }))
  vi.stubGlobal("fetch", fetchMock)
  await open(2); vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] })
  await act(async () => { click("Also pair my CLI Optional — signs your terminal in too.") })
  mocks.resume.mockResolvedValue({ ok: true, state: { ...snapshot, savedCredential: { id: "paired-token", provider: "ANTHROPIC" } } })
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  if (status === "consumed") {
    expect(screen.getByText("CLI paired and model token received.")).toBeInTheDocument()
    expect(mocks.resume).toHaveBeenCalledTimes(2)
    expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled()
    await act(async () => { click("Continue") })
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.validate).not.toHaveBeenCalled()
  } else {
    expect(screen.getByRole("button", { name: "get a new one" })).toBeInTheDocument()
    await act(async () => { click("get a new one") })
    expect(mocks.server.mock.calls.filter(([path]) => path.endsWith("/pair/start"))).toHaveLength(2)
  }
})
it.each(["other-workspace", "other-provider", "missing", "failed"])("does not accept a paired credential from %s", async (kind) => {
  mocks.server.mockImplementation(async (path: string) => reply(path.endsWith("/pair/start") ? { code: "PAIR-CODE", expires_at: new Date(Date.now() + 120000).toISOString() } : { available: true, in_use: true }))
  vi.stubGlobal("fetch", vi.fn(async () => reply({ status: "consumed" })))
  await open(2); vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] })
  await act(async () => { click("Also pair my CLI Optional — signs your terminal in too.") })
  mocks.resume.mockResolvedValue(kind === "failed" ? { ok: false, error: "Try again" } : { ok: true, state: { ...snapshot, workspaceId: kind === "other-workspace" ? "ws-other" : "ws-1", savedCredential: kind === "missing" ? null : { id: "paired", provider: kind === "other-provider" ? "OPENAI" : "ANTHROPIC" } } })
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  expect(screen.getByText("CLI paired.")).toBeInTheDocument()
  mocks.resume.mockResolvedValue({ ok: true, state: { ...snapshot, savedCredential: { id: "paired", provider: "ANTHROPIC" } } })
  await act(async () => { await vi.advanceTimersByTimeAsync(2500) })
  expect(screen.getByRole("button", { name: "Continue" })).toBeEnabled()
})
it("keeps telemetry choice in the final request with reduced motion enabled", async () => {
  mocks.reducedMotion = true
  await open(3); click("Back"); await screen.findByLabelText("Claude Code CLI token")
  fireEvent.click(screen.getByRole("checkbox")); click("Continue")
  await screen.findByRole("region", { name: "Guide" }); click("Apply proposal"); click("Prepare proposal")
  expect(await screen.findByText(/one more waiting/)).toBeInTheDocument()
  click("Launch"); await screen.findByRole("heading", { name: "Your crew is ready" })
  expect(posted("/api/v1/onboarding/setup")).toMatchObject({ telemetry_opt_in: true })
})
it("expires pairing locally, copies the complete command, and stops polling in browser mode", async () => {
  const clipboard = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined)
  vi.stubGlobal("isSecureContext", true)
  mocks.server.mockImplementation(async (path: string) => reply(path.endsWith("/pair/start") ? { code: "SHORT-CODE", expires_at: new Date(Date.now() + 3000).toISOString() } : { available: true, in_use: true }))
  const fetchMock = vi.fn().mockRejectedValue(new Error("transient")); vi.stubGlobal("fetch", fetchMock)
  await open(2); vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] })
  await act(async () => { click("Also pair my CLI Optional — signs your terminal in too.") })
  await act(async () => { click("Copy command") })
  expect(clipboard).toHaveBeenCalledWith(`crewship login --pair --code=SHORT-CODE --server=${window.location.origin}`)
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(screen.getByRole("button", { name: "get a new one" })).toBeInTheDocument()
  await act(async () => { click("get a new one") })
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: /Chat in browser/ })) })
  const count = fetchMock.mock.calls.length
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  expect(fetchMock).toHaveBeenCalledTimes(count)
})
it("ignores a credential response after leaving CLI mode", async () => {
  mocks.server.mockImplementation(async (path: string) => reply(path.endsWith("/pair/start") ? { code: "PAIR-CODE", expires_at: new Date(Date.now() + 120000).toISOString() } : { available: true, in_use: true }))
  vi.stubGlobal("fetch", vi.fn(async () => reply({ status: "consumed" })))
  await open(2); vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] })
  let finish!: (value: unknown) => void
  mocks.resume.mockReturnValue(new Promise((resolve) => { finish = resolve }))
  await act(async () => { click("Also pair my CLI Optional — signs your terminal in too.") })
  await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: /Chat in browser/ })) })
  await act(async () => { finish({ ok: true, state: { ...snapshot, savedCredential: { id: "late", provider: "ANTHROPIC" } } }) })
  expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled()
  expect(screen.queryByText("Your saved Anthropic credential will be reused.")).toBeNull()
})
