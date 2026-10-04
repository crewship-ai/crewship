import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { AddCredentialDialog } from "../add-credential-dialog"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
const props = () => ({ workspaceId: "workspace", open: true, onOpenChange: vi.fn(), onSuccess: vi.fn() })
function value(text = "test-input") { fireEvent.change(screen.getByLabelText("API Key", { selector: "input" }), { target: { value: text } }) }
function submit() { fireEvent.submit(screen.getByRole("button", { name: "Add Credential" }).closest("form")!) }
async function select(label: string, option: string) { fireEvent.click(screen.getByLabelText(label)); fireEvent.keyDown(screen.getByLabelText(label), { key: "ArrowDown" }); fireEvent.click(await screen.findByRole("option", { name: option })) }
function deferred() { let resolve!: (r: Response) => void; const promise = new Promise<Response>(yes => { resolve = yes }); return { promise, resolve } }
beforeEach(() => { api.mockReset(); api.mockResolvedValue(response({})) })
afterEach(cleanup)
it("submits provider defaults with optional trimmed label and preserves the secret value", async () => {
 const p = props(); render(<AddCredentialDialog {...p} />); value(" test-input ")
 fireEvent.change(screen.getByLabelText("Label (optional)"), { target: { value: " Production " } }); submit()
 await waitFor(() => expect(p.onSuccess).toHaveBeenCalledOnce())
 expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({ name: "ANTHROPIC_API_KEY", value: " test-input ", type: "API_KEY", provider: "ANTHROPIC", scope: "WORKSPACE", account_label: "Production" })
 expect(p.onOpenChange).toHaveBeenCalledWith(false)
})
it.each([["OpenAI (GPT / Codex)", "OPENAI_API_KEY"], ["Google (Gemini)", "GOOGLE_API_KEY"], ["Cursor", "CURSOR_API_KEY"], ["Factory (Droid)", "FACTORY_API_KEY"]])("selects %s and its environment name", async (provider, name) => {
 render(<AddCredentialDialog {...props()} />); await select("Provider", provider); expect(screen.getByLabelText("Name (env variable)")).toHaveValue(name)
})
it.each([["GitHub", "GH_TOKEN"], ["GitLab", "GITLAB_TOKEN"], ["Vercel", "VERCEL_TOKEN"], ["AWS", "AWS_ACCESS_KEY_ID"]])("selects CLI provider %s", async (provider, name) => {
 render(<AddCredentialDialog {...props()} />); fireEvent.click(screen.getByRole("button", { name: "CLI Token" })); await select("Provider", provider); expect(screen.getByLabelText("Name (env variable)")).toHaveValue(name)
})
it("submits custom CLI and secrets with editable names", async () => {
 const p = props(); render(<AddCredentialDialog {...p} />); fireEvent.click(screen.getByRole("button", { name: "CLI Token" }));
 fireEvent.change(screen.getByLabelText("Name (env variable)"), { target: { value: " CUSTOM_TOKEN " } }); fireEvent.change(screen.getByLabelText("Value"), { target: { value: "opaque" } }); submit()
 await waitFor(() => expect(p.onSuccess).toHaveBeenCalledOnce()); expect(JSON.parse(api.mock.calls[0][1].body)).toMatchObject({ name: "CUSTOM_TOKEN", provider: "CUSTOM_CLI", type: "CLI_TOKEN" })
 fireEvent.click(screen.getByRole("button", { name: "Secret" })); fireEvent.change(screen.getByLabelText("Name (env variable)"), { target: { value: "SECRET_NAME" } }); fireEvent.change(screen.getByLabelText("Value"), { target: { value: "opaque" } }); fireEvent.change(screen.getByLabelText("Description"), { target: { value: " Purpose " } }); submit()
 await waitFor(() => expect(p.onSuccess).toHaveBeenCalledTimes(2)); expect(JSON.parse(api.mock.calls[1][1].body)).toMatchObject({ type: "SECRET", provider: "NONE", description: "Purpose" })
})
it("validates blank names and values without sending", () => {
 render(<AddCredentialDialog {...props()} />); submit(); expect(screen.getByText("Value is required")).toBeInTheDocument()
 fireEvent.click(screen.getByRole("button", { name: "Secret" })); submit(); expect(screen.getByText("Name is required")).toBeInTheDocument(); expect(api).not.toHaveBeenCalled()
})
it("shows setup-token guidance, hides unsupported key testing, and toggles visibility", () => {
 render(<AddCredentialDialog {...props()} />); fireEvent.click(screen.getByRole("button", { name: "AI CLI Token" })); expect(screen.getByText("claude setup-token")).toBeInTheDocument()
 fireEvent.change(screen.getByLabelText("Setup Token"), { target: { value: "sk-ant-oat-fixture" } }); expect(screen.queryByRole("button", { name: "Test Key" })).not.toBeInTheDocument()
 fireEvent.click(screen.getByRole("button", { name: "Show value" })); expect(screen.getByLabelText("Setup Token")).toHaveAttribute("type", "text"); fireEvent.click(screen.getByRole("button", { name: "Hide value" })); expect(screen.getByLabelText("Setup Token")).toHaveAttribute("type", "password")
})
it.each(["valid", "invalid", "detail", "http", "network"])("reports key-test outcome %s", async outcome => {
 if (outcome === "network") api.mockRejectedValue(new Error("offline")); else api.mockResolvedValue(response({ valid: outcome === "valid", error: outcome === "detail" ? "Rejected" : undefined }, outcome === "http" ? 500 : 200))
 render(<AddCredentialDialog {...props()} />); value(" test-input "); fireEvent.click(screen.getByRole("button", { name: "Test Key" }));
 const expected = { valid: "Valid", invalid: "Invalid", detail: "Rejected", http: "Test request failed", network: "Network error" }[outcome]!
 await screen.findByText(expected); expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({ provider: "ANTHROPIC", type: "API_KEY", value: "test-input" }); value("different"); expect(screen.queryByText(expected)).not.toBeInTheDocument()
})
it.each(["detail", "fallback", "network"])("keeps submission editable after %s failure", async outcome => {
 if (outcome === "network") api.mockRejectedValue(new Error("offline")); else api.mockResolvedValue(response({ error: outcome === "detail" ? "Name already exists" : {} }, 400))
 const p = props(); render(<AddCredentialDialog {...p} />); value(); submit(); await screen.findByText(outcome === "detail" ? "Name already exists" : outcome === "fallback" ? "Failed to create credential" : "Network error. Please try again.")
 expect(screen.getByRole("button", { name: "Add Credential" })).toBeEnabled(); expect(p.onSuccess).not.toHaveBeenCalled()
})
it("restores the default environment name after cancel and reopening", () => {
 const p = props(); const view = render(<AddCredentialDialog {...p} />); value(); fireEvent.click(screen.getByRole("button", { name: "Cancel" })); view.rerender(<AddCredentialDialog {...p} open={false} />); view.rerender(<AddCredentialDialog {...p} />)
 expect(screen.getByLabelText("Name (env variable)")).toHaveValue("ANTHROPIC_API_KEY"); expect(screen.getByLabelText("API Key", { selector: "input" })).toHaveValue("")
})
it("discards a previous value's delayed validation", async () => {
 const pending = deferred(); api.mockReturnValue(pending.promise); render(<AddCredentialDialog {...props()} />); value(); fireEvent.click(screen.getByRole("button", { name: "Test Key" })); value("replacement"); await act(async () => pending.resolve(response({ valid: true }))); expect(screen.queryByText("Valid")).not.toBeInTheDocument(); expect(screen.getByRole("button", { name: "Test Key" })).toBeEnabled()
})
it("does not let a previous workspace save close the current dialog", async () => {
 const pending = deferred(); api.mockReturnValue(pending.promise); const p = props(); const view = render(<AddCredentialDialog {...p} />); value(); submit(); view.rerender(<AddCredentialDialog {...p} workspaceId="next" />); await act(async () => pending.resolve(response({}))); expect(p.onSuccess).not.toHaveBeenCalled(); expect(p.onOpenChange).not.toHaveBeenCalled(); expect(screen.getByRole("button", { name: "Add Credential" })).toBeEnabled(); expect(screen.getByLabelText("API Key", { selector: "input" })).toHaveValue("")
})
it("selects, removes and submits only the chosen crews", async () => {
 api.mockImplementation(async (_url: string, init?: RequestInit) => response(init?.method ? {} : [{ id: "alpha", name: "Alpha" }, { id: "beta", name: "Beta" }]))
 const p = props(); render(<AddCredentialDialog {...p} />); value(); await select("Scope", "Crew"); submit(); expect(screen.getByText("At least one crew is required for crew-scoped credentials")).toBeInTheDocument()
 fireEvent.click(await screen.findByText("Select crews...")); fireEvent.click(await screen.findByRole("option", { name: "Alpha" })); fireEvent.click(screen.getByRole("option", { name: "Beta" })); expect(screen.getByText("2 crews selected")).toBeInTheDocument()
 fireEvent.click(screen.getByRole("option", { name: "Alpha" })); fireEvent.click(screen.getByText("1 crew selected")); fireEvent.click(screen.getByText("Beta"));
 fireEvent.click(screen.getByText("Select crews...")); fireEvent.click(screen.getByRole("option", { name: "Alpha" })); fireEvent.click(screen.getByText("1 crew selected")); submit()
 await waitFor(() => expect(p.onSuccess).toHaveBeenCalledOnce()); const call = api.mock.calls.find(([, init]) => init?.method === "POST")!; expect(JSON.parse(call[1].body).crew_ids).toEqual(["alpha"])
})
it.each(["empty", "malformed", "http", "network"])("handles %s crew inventory without granting access", async outcome => {
 api.mockImplementation(async () => { if (outcome === "network") throw new Error("offline"); return response(outcome === "empty" ? [] : {}, outcome === "http" ? 503 : 200) })
 render(<AddCredentialDialog {...props()} />); await select("Scope", "Crew"); fireEvent.click(await screen.findByText("Select crews...")); expect(screen.getByText("No crews found.")).toBeInTheDocument()
})
it("encodes workspace scope and ignores abandoned crew requests", async () => {
 const pending = deferred(); api.mockReturnValueOnce(pending.promise).mockImplementation(async () => response([{ id: "new", name: "New crew" }]))
 const p = props(); const view = render(<AddCredentialDialog {...p} workspaceId="old & other" />); await select("Scope", "Crew"); expect(new URL(api.mock.calls[0][0], "http://localhost").searchParams.get("workspace_id")).toBe("old & other")
 view.rerender(<AddCredentialDialog {...p} workspaceId="new" />); await select("Scope", "Crew"); await act(async () => pending.resolve(response([{ id: "old", name: "Old crew" }]))); fireEvent.click(await screen.findByText("Select crews...")); expect(screen.getByRole("option", { name: "New crew" })).toBeInTheDocument(); expect(screen.queryByText("Old crew")).not.toBeInTheDocument()
})
it("clears crew grants when returning to workspace scope", async () => {
 api.mockResolvedValue(response([{ id: "one", name: "One" }])); render(<AddCredentialDialog {...props()} />); await select("Scope", "Crew"); fireEvent.click(await screen.findByText("Select crews...")); fireEvent.click(screen.getByRole("option", { name: "One" })); fireEvent.click(screen.getByText("1 crew selected")); await select("Scope", "Workspace"); expect(screen.queryByText("One")).not.toBeInTheDocument()
})
it.each(["http", "network"])("ignores %s failures from a previous workspace save", async failure => {
 let reject!: (reason: Error) => void; const pending = deferred(); const network = new Promise<Response>((_resolve, no) => { reject = no }); api.mockReturnValue(failure === "network" ? network : pending.promise)
 const p = props(); const view = render(<AddCredentialDialog {...p} />); value(); submit(); view.rerender(<AddCredentialDialog {...p} workspaceId="next" />)
 await act(async () => { if (failure === "network") reject(new Error("offline")); else pending.resolve(response({ error: "Old failure" }, 400)) }); expect(screen.queryByText(/Old failure|Network error/)).not.toBeInTheDocument(); expect(p.onSuccess).not.toHaveBeenCalled()
})
it("discards validation after the dialog is closed and reopened", async () => {
 const pending = deferred(); api.mockReturnValue(pending.promise); const p = props(); const view = render(<AddCredentialDialog {...p} />); value(); fireEvent.click(screen.getByRole("button", { name: "Test Key" })); view.rerender(<AddCredentialDialog {...p} open={false} />); view.rerender(<AddCredentialDialog {...p} />); value("new-input"); await act(async () => pending.resolve(response({ valid: true }))); expect(screen.queryByText("Valid")).not.toBeInTheDocument(); expect(screen.getByRole("button", { name: "Test Key" })).toBeEnabled()
})
it("encodes the workspace on writes without allowing query injection", async () => {
 const p = { ...props(), workspaceId: "one&workspace_id=other" }; render(<AddCredentialDialog {...p} />); value(); submit(); await waitFor(() => expect(p.onSuccess).toHaveBeenCalledOnce()); expect(new URL(api.mock.calls[0][0], "http://localhost").searchParams.getAll("workspace_id")).toEqual([p.workspaceId])
})
