import { beforeEach, describe, expect, it, vi } from "vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { SkillEditor, type SkillEditorTarget } from "@/components/features/skills/skill-editor"
import type { SkillDetail } from "@/components/features/skills/skills-model"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", async (importActual) => ({
  ...(await importActual<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetch(...a),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))

const ok = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }))

function setup(target: SkillEditorTarget, taken: string[] = []) {
  const onSaved = vi.fn()
  const onClose = vi.fn()
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { mutations: { retry: false } } })}>
      <SkillEditor target={target} workspaceId="ws_1" takenSlugs={new Set(taken)} onClose={onClose} onSaved={onSaved} />
    </QueryClientProvider>,
  )
  return { onSaved, onClose }
}

const sentContent = (call: number) => JSON.parse(apiFetch.mock.calls[call][1].body).content as string

const skill: SkillDetail = {
  id: "sk_1", name: "File Crafter", slug: "file-crafter", display_name: "File Crafter", description: "Create files",
  version: "1.2.0", author: "ops", category: "CODING", source: "CUSTOM", icon: "terminal", tags: '["files"]',
  needs_credentials: ["GH_TOKEN"], content: "## Instructions\n1. Write it", license: "MIT", agent_count: 0,
}

describe("SkillEditor", () => {
  beforeEach(() => apiFetch.mockReset())

  it("writes a skill by hand with the icon and domain picked here", async () => {
    apiFetch.mockImplementation(() => ok({ skill_id: "sk_new", slug: "invoice-matcher", name: "Invoice Matcher", created: true }, 201))
    const { onSaved } = setup({ kind: "new" })

    fireEvent.click(screen.getByRole("radio", { name: /Write it/ }))
    fireEvent.change(screen.getByPlaceholderText("Invoice matcher"), { target: { value: "Invoice Matcher" } })
    fireEvent.click(screen.getByRole("radio", { name: "Finance" }))
    fireEvent.change(screen.getByLabelText(/When should agents use it/), { target: { value: "Match payments: by amount" } })
    fireEvent.change(screen.getByLabelText(/Credentials it needs/), { target: { value: "FAKTUROID_TOKEN" } })

    fireEvent.click(screen.getByRole("button", { name: "Change skill icon" }))
    fireEvent.click(screen.getByRole("radio", { name: "Receipt" }))
    fireEvent.click(screen.getByRole("button", { name: "Use this icon" }))

    fireEvent.click(screen.getByRole("button", { name: "Create skill" }))
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("sk_new"))

    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(apiFetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws_1/skills/import")
    const md = sentContent(0)
    expect(md).toMatch(/^---\nname: invoice-matcher\ndisplay_name: "Invoice Matcher"\ndescription: "Match payments: by amount"\n/)
    expect(md).toContain("category: FINANCE\nicon: receipt\ncredential_requirements:\n  - FAKTUROID_TOKEN\n---")
    expect(md).toContain("## When to Activate")
  })

  it("has Claude write it, then saves the icon and domain the generator ignores", async () => {
    apiFetch
      .mockImplementationOnce(() => ok({ skill_id: "sk_gen", slug: "invoice-matcher", content: "---\nname: Invoice matcher\ndescription: Match\ncategory: DATA\n---\n# Invoice matcher\n" }, 201))
      .mockImplementationOnce(() => ok({ skill_id: "sk_gen", slug: "invoice-matcher", name: "Invoice Matcher", created: false }, 201))
    const { onSaved } = setup({ kind: "new" })

    fireEvent.change(screen.getByPlaceholderText("Invoice matcher"), { target: { value: "Invoice Matcher" } })
    fireEvent.click(screen.getByRole("radio", { name: "Finance" }))
    fireEvent.change(screen.getByLabelText(/What should it do/), { target: { value: "Match incoming payments to open invoices by amount." } })
    fireEvent.click(screen.getByRole("button", { name: "Write skill" }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("sk_gen"))
    expect(apiFetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws_1/skills/generate")
    expect(JSON.parse(apiFetch.mock.calls[0][1].body)).toEqual({ slug: "invoice-matcher", prompt: "Match incoming payments to open invoices by amount." })
    expect(apiFetch.mock.calls[1][0]).toBe("/api/v1/workspaces/ws_1/skills/import")
    expect(sentContent(1)).toBe('---\nname: invoice-matcher\ndescription: Match\ncategory: FINANCE\ndisplay_name: "Invoice Matcher"\n---\n# Invoice matcher\n')
  })

  it("will not let a new skill silently update one that exists", () => {
    setup({ kind: "new" }, ["file-crafter"])
    fireEvent.change(screen.getByPlaceholderText("Invoice matcher"), { target: { value: "File Crafter" } })
    fireEvent.change(screen.getByLabelText(/What should it do/), { target: { value: "Create files and directories for the task." } })
    expect(screen.getByText("A skill called file-crafter already exists. Pick another name.")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Write skill" })).toBeDisabled()
  })

  it("edits under the same slug and keeps what the form does not show", async () => {
    apiFetch.mockImplementation(() => ok({ skill_id: "sk_1", slug: "file-crafter", name: "File Crafter", created: false }, 201))
    const { onSaved } = setup({ kind: "edit", skill })

    expect(screen.queryByRole("radio", { name: /Describe it/ })).not.toBeInTheDocument()
    fireEvent.change(screen.getByPlaceholderText("Invoice matcher"), { target: { value: "File Smith" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("sk_1"))

    const md = sentContent(0)
    expect(md).toContain("name: file-crafter\ndisplay_name: \"File Smith\"")
    expect(md).toContain("version: 1.2.0\nauthor: ops\nlicense: MIT\ncategory: CODING\nicon: terminal")
    expect(md).toContain("credential_requirements:\n  - GH_TOKEN\ntags:\n  - files")
    expect(md.endsWith("## Instructions\n1. Write it\n")).toBe(true)
  })

  it("shows the importer's refusal instead of closing", async () => {
    apiFetch.mockImplementation(() => ok({ detail: "skill \"file-crafter\" is BUNDLED" }, 409))
    const { onSaved } = setup({ kind: "edit", skill })
    fireEvent.change(screen.getByPlaceholderText("Invoice matcher"), { target: { value: "File Smith" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    expect(await screen.findByText(/is BUNDLED/)).toBeInTheDocument()
    expect(onSaved).not.toHaveBeenCalled()
  })

  it("rejects a credential that is not an environment variable name", () => {
    setup({ kind: "edit", skill })
    fireEvent.change(screen.getByLabelText(/Credentials it needs/), { target: { value: "gh-token" } })
    expect(screen.getByText(/gh-token is not an environment variable name/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled()
  })
})
