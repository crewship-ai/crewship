import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { TemplateGallery } from "../template-gallery"
import type { WorkflowTemplate } from "@/lib/types/template"

const state = vi.hoisted(() => ({ api: vi.fn(), success: vi.fn(), error: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: state.api }))
vi.mock("sonner", () => ({ toast: { success: state.success, error: state.error } }))
function template(id: string, overrides: Partial<WorkflowTemplate> = {}): WorkflowTemplate {
  return { id, workspace_id: "a", name: `Template ${id}`, description: null, template_json: { name: id, description: "", steps: [] }, icon: null, color: null, is_builtin: false, created_at: "", updated_at: "", ...overrides }
}
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done }); return { promise, resolve } }
beforeEach(() => { state.api.mockReset(); state.success.mockReset(); state.error.mockReset() })
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
async function openEditor() {
  render(<TemplateGallery workspaceId="a" />)
  fireEvent.click(await screen.findByRole("button", { name: "New Template" }))
}
function fill() {
  fireEvent.change(screen.getByPlaceholderText("e.g. Code Review Pipeline"), { target: { value: "  Review  " } })
  fireEvent.change(screen.getByPlaceholderText("Step title"), { target: { value: "Review changes" } })
}

it("does not restore old workspace templates after a delayed response", async () => {
  const old = deferred<Response>()
  state.api.mockImplementation((url: string) => url.endsWith("=a") ? old.promise : Promise.resolve(json([template("current")])))
  const view = render(<TemplateGallery workspaceId="a" />)
  view.rerender(<TemplateGallery workspaceId="b" />)
  await screen.findByText("Template current")
  await act(async () => old.resolve(json([template("old")])))
  expect(screen.getByText("Template current")).toBeVisible()
  expect(screen.queryByText("Template old")).not.toBeInTheDocument()
})

it("clears the old list and editor when the workspace changes", async () => {
  const next = deferred<Response>()
  state.api.mockImplementation((url: string) => url.endsWith("=a") ? Promise.resolve(json([template("old")])) : next.promise)
  const view = render(<TemplateGallery workspaceId="a" />)
  fireEvent.click(await screen.findByRole("button", { name: "New Template" }))
  fill()
  view.rerender(<TemplateGallery workspaceId="b" />)
  expect(screen.queryByText("Template old")).not.toBeInTheDocument()
  expect(screen.queryByDisplayValue("  Review  ")).not.toBeInTheDocument()
  await act(async () => next.resolve(json([])))
  expect(screen.getByText("No templates yet")).toBeVisible()
})

it.each(["refused", "network", "invalid json"])("distinguishes a %s read from an empty template catalog", async (failure) => {
  state.api.mockImplementation(() => failure === "network" ? Promise.reject(new Error("offline")) : Promise.resolve(failure === "refused" ? json({}, 503) : new Response("bad json")))
  render(<TemplateGallery workspaceId="a" />)
  expect(await screen.findByRole("alert")).toHaveTextContent("Could not load templates")
  expect(screen.queryByText("No templates yet")).not.toBeInTheDocument()
  state.api.mockResolvedValue(json([]))
  fireEvent.click(screen.getByRole("button", { name: "Retry" }))
  expect(await screen.findByText("No templates yet")).toBeVisible()
})

it("validates required names and titles before sending a creation request", async () => {
  state.api.mockResolvedValue(json([]))
  await openEditor()
  fireEvent.click(screen.getByRole("button", { name: "Create Template" }))
  expect(state.error).toHaveBeenCalledWith("Template name is required")
  fireEvent.change(screen.getByPlaceholderText("e.g. Code Review Pipeline"), { target: { value: "Review" } })
  fireEvent.click(screen.getByRole("button", { name: "Create Template" }))
  expect(state.error).toHaveBeenCalledWith("All steps need a title")
  expect(state.api).toHaveBeenCalledTimes(1)
})

it("creates a trimmed template with a default agent role and refreshes the catalog", async () => {
  state.api.mockImplementation((_url: string, init?: RequestInit) => Promise.resolve(json(init?.method === "POST" ? {} : [])))
  await openEditor()
  fill()
  fireEvent.click(screen.getByRole("button", { name: "Create Template" }))
  await waitFor(() => expect(state.success).toHaveBeenCalledWith("Template created"))
  const request = state.api.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(request[0]).toBe("/api/v1/templates?workspace_id=a")
  expect(JSON.parse(request[1].body)).toEqual({ name: "Review", description: null, template_json: { name: "Review", description: "", steps: [{ id: "step-1", title: "Review changes", agent_role: "AGENT", depends_on: [] }] } })
  expect(screen.queryByText("New Workflow Template")).not.toBeInTheDocument()
})

it("removes dependencies on deleted steps without reusing an existing step ID", async () => {
  state.api.mockImplementation((_url: string, init?: RequestInit) => Promise.resolve(json(init?.method === "POST" ? {} : [])))
  await openEditor()
  fill()
  fireEvent.change(screen.getByPlaceholderText("Optional description"), { target: { value: "  Team review  " } })
  fireEvent.click(screen.getByRole("button", { name: "Add Step" }))
  fireEvent.click(screen.getByRole("button", { name: "Add Step" }))
  fireEvent.click(screen.getByRole("button", { name: "Remove step 2" }))
  fireEvent.click(screen.getByRole("button", { name: "Add Step" }))
  screen.getAllByPlaceholderText("Step title").forEach((input, i) => fireEvent.change(input, { target: { value: `Task ${i}` } }))
  fireEvent.change(screen.getAllByPlaceholderText("Role")[1], { target: { value: "REVIEWER" } })
  fireEvent.click(screen.getByRole("button", { name: "Create Template" }))
  await waitFor(() => expect(state.success).toHaveBeenCalled())
  const body = JSON.parse(state.api.mock.calls.find(([, init]) => init?.method === "POST")![1].body)
  expect(body.description).toBe("Team review")
  expect(body.template_json.steps).toEqual([
    { id: "step-1", title: "Task 0", agent_role: "AGENT", depends_on: [] },
    { id: "step-3", title: "Task 1", agent_role: "REVIEWER", depends_on: [] },
    { id: "step-4", title: "Task 2", agent_role: "AGENT", depends_on: ["step-3"] },
  ])
})

it.each([true, false])("reports a refused create response (readable reason: %s) and preserves the draft", async (readable) => {
  state.api.mockImplementation((_url: string, init?: RequestInit) => Promise.resolve(init?.method === "POST" ? readable ? json({ error: "Template quota reached" }, 409) : new Response("proxy error", { status: 502 }) : json([])))
  await openEditor()
  fill()
  fireEvent.click(screen.getByRole("button", { name: "Create Template" }))
  await waitFor(() => expect(state.error).toHaveBeenCalledWith(readable ? "Template quota reached" : "Failed to create template"))
  expect(screen.getByDisplayValue("Review changes")).toBeVisible()
  expect(screen.getByRole("button", { name: "Create Template" })).toBeEnabled()
})

it("cancels either editor close control without writing", async () => {
  state.api.mockResolvedValue(json([]))
  await openEditor()
  fireEvent.click(screen.getByRole("button", { name: "Close editor" }))
  expect(screen.queryByText("New Workflow Template")).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: "New Template" }))
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  expect(screen.queryByText("New Workflow Template")).not.toBeInTheDocument()
  expect(state.api).toHaveBeenCalledTimes(1)
})

it("renders dependent, parallel, loop and empty templates without granting deletion for builtins", async () => {
  state.api.mockResolvedValue(json([
    template("built", { is_builtin: true, icon: "git-branch", color: "#abcdef", description: "Review flow", template_json: { name: "flow", description: "", steps: [
      { id: "a", title: "Start", agent_role: "Team Lead" },
      { id: "b", title: "Second", max_iterations: 2, depends_on: [] },
      { id: "c", title: "Long reviewer title", depends_on: ["a", "b"] },
      { id: "d", title: "", depends_on: ["c", "missing"] },
    ] } }),
    template("single", { icon: "unknown", template_json: { name: "one", description: "", steps: [{ id: "one", title: "One" }] } }),
    template("empty"),
  ]))
  render(<TemplateGallery workspaceId="a" />)
  expect(await screen.findByText("3 templates available")).toBeVisible()
  expect(screen.getByText("Loop")).toBeVisible()
  expect(screen.getByText("Parallel")).toBeVisible()
  expect(screen.getByText("Long rev…")).toBeVisible()
  expect(screen.getByText("1 step")).toBeVisible()
  expect(screen.queryByRole("button", { name: "Delete template Template built" })).not.toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Delete template Template empty" })).toBeEnabled()
})

it("requires confirmation before deletion and reports refusal before a successful retry", async () => {
  let deleted = false
  let refuse = true
  state.api.mockImplementation((_url: string, init?: RequestInit) => {
    if (init?.method === "DELETE") { if (refuse) return Promise.resolve(json({}, 403)); deleted = true; return Promise.resolve(json({})) }
    return Promise.resolve(json(deleted ? [] : [template("custom")]))
  })
  const confirm = vi.fn().mockReturnValue(false)
  vi.stubGlobal("confirm", confirm)
  render(<TemplateGallery workspaceId="a" />)
  fireEvent.click(await screen.findByRole("button", { name: "Delete template Template custom" }))
  expect(state.api).toHaveBeenCalledTimes(1)
  confirm.mockReturnValue(true)
  fireEvent.click(screen.getByRole("button", { name: "Delete template Template custom" }))
  await waitFor(() => expect(state.error).toHaveBeenCalledWith("Failed to delete template"))
  refuse = false
  fireEvent.click(screen.getByRole("button", { name: "Delete template Template custom" }))
  await screen.findByText("No templates yet")
  expect(state.success).toHaveBeenCalledWith("Template deleted")
})

it("reports a network failure during creation and keeps the draft retryable", async () => {
  state.api.mockImplementation((_url: string, init?: RequestInit) => init?.method === "POST" ? Promise.reject(new Error("offline")) : Promise.resolve(json([])))
  await openEditor()
  fill()
  fireEvent.click(screen.getByRole("button", { name: "Create Template" }))
  await waitFor(() => expect(state.error).toHaveBeenCalledWith("Failed to create template"))
  expect(screen.getByDisplayValue("Review changes")).toBeVisible()
  expect(screen.getByRole("button", { name: "Create Template" })).toBeEnabled()
})

it("ignores a completed creation after the editor is abandoned", async () => {
  const created = deferred<Response>()
  state.api.mockImplementation((_url: string, init?: RequestInit) => init?.method === "POST" ? created.promise : Promise.resolve(json([])))
  await openEditor()
  fill()
  fireEvent.click(screen.getByRole("button", { name: "Create Template" }))
  expect(screen.getByRole("button", { name: "Create Template" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
  await act(async () => created.resolve(json({})))
  expect(state.success).not.toHaveBeenCalled()
  expect(state.api).toHaveBeenCalledTimes(2)
})

it("reports a network failure during deletion without losing the template", async () => {
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true))
  state.api.mockImplementation((_url: string, init?: RequestInit) => init?.method === "DELETE" ? Promise.reject(new Error("offline")) : Promise.resolve(json([template("custom")])))
  render(<TemplateGallery workspaceId="a" />)
  fireEvent.click(await screen.findByRole("button", { name: "Delete template Template custom" }))
  await waitFor(() => expect(state.error).toHaveBeenCalledWith("Failed to delete template"))
  expect(screen.getByText("Template custom")).toBeVisible()
})

it("does not refresh a former workspace when its deletion finishes", async () => {
  vi.stubGlobal("confirm", vi.fn().mockReturnValue(true))
  const deleted = deferred<Response>()
  state.api.mockImplementation((url: string, init?: RequestInit) => init?.method === "DELETE" ? deleted.promise : Promise.resolve(json([template(url.endsWith("=a") ? "old" : "current")])))
  const view = render(<TemplateGallery workspaceId="a" />)
  fireEvent.click(await screen.findByRole("button", { name: "Delete template Template old" }))
  view.rerender(<TemplateGallery workspaceId="b" />)
  await screen.findByText("Template current")
  await act(async () => deleted.resolve(json({})))
  expect(state.success).not.toHaveBeenCalled()
  expect(screen.getByText("Template current")).toBeVisible()
  expect(state.api).toHaveBeenCalledTimes(3)
})
