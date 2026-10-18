import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, within, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { MemberResourceAccess } from "../member-resource-access"
import { PageSaveProvider, PageSaveBar } from "@/components/ui/page-save-bar"
import type { ResourcePolicy } from "@/lib/resource-sharing"

const fetchMock = vi.hoisted(() => vi.fn())
const success = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("sonner", () => ({ toast: { success, error: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
afterEach(() => { cleanup(); fetchMock.mockReset(); success.mockReset() })
const policy: ResourcePolicy = { membership_id: "member", revision: 7, mode: "restricted", rights: [], grant_details: [], preset_selections: [] }
const response = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
const digest = "a".repeat(64)
const catalog = { presets: ["project", "routine"].map(kind => ({ definition_hash: `${kind}-v1`, definition: {
  preset_id: `${kind}-levels`, version: 1, kind,
  managed_operations: kind === "project" ? ["list", "read", "contribute", "manage"] : ["view", "results", "run", "edit"],
  levels: kind === "project" ? [{ level: "view", operations: ["list", "read"] }, { level: "manage", operations: ["list", "read", "contribute", "manage"] }] : [{ level: "view", operations: ["view", "results"] }, { level: "edit", operations: ["view", "results", "run", "edit"] }],
} })) }
type Options = { policy?: ResourcePolicy; policyReadStatus?: number; policyReadResponse?: ResourcePolicy; previewStatus?: number; applyStatus?: number; corruptPreview?: boolean; applyThrows?: boolean }
function show(options: Options = {}) {
  const original = options.policy ?? policy
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const input = JSON.parse(String(init.body))
      if (url.includes("/presets/preview")) {
        if (options.previewStatus) return response({}, options.previewStatus)
        const kind = input.resource.kind
        const definition = catalog.presets.find(item => item.definition.kind === kind)!
        const operations = definition.definition.levels.find(item => item.level === input.level)!.operations
        return response({ selection: { ...input, membership_id: options.corruptPreview ? "other-member" : input.membership_id }, definition_hash: `${kind}-v1`, current_selection: null,
          before: original.grant_details ?? [], changes: operations.map(operation => ({ right: { kind, id: input.resource.id, operation }, action: "add", issuer_id: "current-owner", live: true })),
          after_rights: [...original.rights.filter(right => !definition.definition.managed_operations.includes(right.operation)), ...operations.map(operation => ({ kind, id: input.resource.id, operation }))],
          orthogonal_rights: original.grant_details?.filter(right => !definition.definition.managed_operations.includes(right.operation)) ?? [],
          warnings: kind === "routine" ? [{ code: "ROUTINE_RESULTS_HISTORY", message: "Past and future regular outputs may include input material; private audiences still apply." }] : [{ code: "RETAINED_DELETE_ISSUES", message: "Separate destructive share remains unchanged.", before_eligible: false, after_eligible: true }], confirmation_digest: digest,
        })
      }
      if (options.applyThrows) throw new Error("private response must never be displayed")
      if (options.applyStatus) return response({}, options.applyStatus)
      const selection = input.selection
      const definition = catalog.presets.find(item => item.definition.kind === selection.resource.kind)!
      const operations = definition.definition.levels.find(item => item.level === selection.level)!.operations
      const rights = [...original.rights.filter(right => right.kind !== selection.resource.kind || right.id !== selection.resource.id || !definition.definition.managed_operations.includes(right.operation)), ...operations.map(operation => ({ kind: selection.resource.kind, id: selection.resource.id, operation }))]
      return response({ ...original, rights, revision: 9, preset_selections: [{ id: "saved-choice", kind: selection.resource.kind, resource_id: selection.resource.id, preset_id: selection.preset_id, version: selection.preset_version,
        level: selection.level, definition_hash: `${selection.resource.kind}-v1`, selected_by: "current-owner", selected_at: "2026-10-17T00:00:00Z", state: "selected" }] })
    }
    if (url.includes("/access/presets")) return response(catalog)
    if (url.includes("/access/effective")) return response({ membership_id: "member", revision: original.revision, account_restricted: original.mode === "restricted", explanation: "Current policy", decisions: [] })
    if (url.includes("/members/member/access")) return response(options.policyReadStatus ? {} : options.policyReadResponse ?? original, options.policyReadStatus ?? 200)
    if (url.includes("/projects")) return response([{ id: "project", name: "Project" }])
    if (url.includes("/pipelines")) return response([{ id: "routine", name: "Routine" }])
    return response([])
  })
  const layout = (memberId = "member") => <QueryClientProvider client={client}><PageSaveProvider><MemberResourceAccess workspaceId="workspace" memberId={memberId} label="Guest" role="MEMBER" /><PageSaveBar /></PageSaveProvider></QueryClientProvider>
  const view = render(layout())
  return Object.assign(client, { rerenderMember: (memberId: string) => view.rerender(layout(memberId)) })
}
async function choose(kind = "project", level = "manage") {
  const open = screen.queryByRole("button", { name: "Edit resource access" })
  if (open) fireEvent.click(open)
  fireEvent.change(await screen.findByLabelText("Resource kind"), { target: { value: kind } })
  await within(screen.getByLabelText("Resource")).findByRole("option", { name: kind === "project" ? "Project" : "Routine" })
  fireEvent.change(screen.getByLabelText("Resource"), { target: { value: kind } })
  await waitFor(() => expect(screen.getByRole("combobox", { name: "Ordered preset" })).toBeEnabled())
  fireEvent.change(screen.getByRole("combobox", { name: "Ordered preset" }), { target: { value: JSON.stringify([`${kind}-levels`, 1, level]) } })
}
const posts = () => fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")
it("offers explicit ordered presets without deriving a choice or making a write", async () => {
  show(); await choose()
  expect(screen.getByText("Custom — no preset was explicitly selected")).toBeVisible()
  expect(screen.getByRole("option", { name: "manage v1 · list + read + contribute + manage" })).toBeVisible()
  expect(posts()).toHaveLength(0)
})
it("requires review and confirmation, sends exactly the preview consent and keeps unrelated metadata", async () => {
  const client = show(); await choose()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  await screen.findByLabelText("Preset consequences")
  expect(screen.getByText(/Eligibility false → true/)).toBeVisible()
  const apply = screen.getByRole("button", { name: "Apply confirmed preset" })
  expect(apply).toBeDisabled(); expect(posts()).toHaveLength(1)
  fireEvent.click(screen.getByRole("checkbox", { name: "I reviewed these exact changes and consequences" }))
  fireEvent.click(apply)
  await waitFor(() => expect(success).toHaveBeenCalledWith("Preset applied"))
  expect(posts()).toHaveLength(2)
  const preview = JSON.parse(String(posts()[0][1].body))
  expect(JSON.parse(String(posts()[1][1].body))).toEqual({ selection: preview, confirmation_digest: digest })
  expect(client.getQueryData<ResourcePolicy>(["member-resource-access", "workspace", "member"])?.preset_selections?.[0].selected_by).toBe("current-owner")
})
it("shows Results history in immutable Routine View and excludes future publication", async () => {
  show(); await choose("routine", "view")
  expect(screen.getByRole("option", { name: "view v1 · view + results" })).toBeVisible()
  expect(screen.queryByRole("option", { name: /publish/ })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  expect(await screen.findByText(/Past and future regular outputs/)).toBeVisible()
})
it.each(["preview", "apply"])("never refreshes or retries a stale %s", async stage => {
  show(stage === "preview" ? { previewStatus: 409 } : { applyStatus: 409 }); await choose()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  if (stage === "apply") {
    fireEvent.click(await screen.findByRole("checkbox", { name: "I reviewed these exact changes and consequences" }))
    fireEvent.click(screen.getByRole("button", { name: "Apply confirmed preset" }))
  }
  expect(await screen.findByRole("alert")).toHaveTextContent("Reload saved policy")
  expect(screen.getByRole("button", { name: "Preview preset" })).toBeDisabled()
  expect(posts()).toHaveLength(stage === "preview" ? 1 : 2)
  expect(fetchMock.mock.calls.filter(([url]) => url.endsWith("/access?workspace_id=workspace"))).toHaveLength(1)
  expect(success).not.toHaveBeenCalled()
})
it("rejects a preview bound to another membership", async () => {
  show({ corruptPreview: true }); await choose(); fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  expect(await screen.findByRole("alert")).toHaveTextContent("Preview unavailable")
  expect(screen.queryByRole("button", { name: "Apply confirmed preset" })).toBeNull()
})
it("preserves inactive issuers unless one retained managed grant is explicitly reissued", async () => {
  const right = { kind: "project" as const, id: "project", operation: "read" }
  show({ policy: { ...policy, rights: [right], grant_details: [{ ...right, issuer_id: "former-owner", created_at: "2026-10-14T00:00:00Z", live: false }] } }); await choose()
  const renewal = screen.getByRole("checkbox", { name: /Reissue read as me.*inactive.*former-owner/ })
  expect(renewal).not.toBeChecked()
  fireEvent.click(renewal); fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  await screen.findByLabelText("Preset consequences")
  expect(JSON.parse(String(posts()[0][1].body)).reissue).toEqual([right])
  expect(screen.queryByRole("checkbox", { name: /Reissue delete_issues/ })).toBeNull()
})
it("keeps manual drafts intact and blocks preset consent until save or discard", async () => {
  const right = { kind: "project" as const, id: "project", operation: "read" }
  show({ policy: { ...policy, rights: [right] } }); await choose()
  fireEvent.click(screen.getByRole("button", { name: "Remove project project read" }))
  expect(screen.getByRole("combobox", { name: "Ordered preset" })).toBeDisabled()
  expect(screen.getByRole("button", { name: "Preview preset" })).toBeDisabled()
  expect(screen.getByRole("region", { name: "Unsaved changes" })).toBeVisible()
  expect(posts()).toHaveLength(0)
})
it("requires reload after an unknown apply outcome without exposing native errors", async () => {
  show({ applyThrows: true }); await choose(); fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  fireEvent.click(await screen.findByRole("checkbox", { name: "I reviewed these exact changes and consequences" }))
  fireEvent.click(screen.getByRole("button", { name: "Apply confirmed preset" }))
  expect(await screen.findByRole("alert")).toHaveTextContent("Apply could not be confirmed")
  expect(screen.getByRole("button", { name: "Preview preset" })).toBeDisabled()
  expect(screen.queryByText(/private response/)).toBeNull(); expect(success).not.toHaveBeenCalled()
})
it("does not convert trusted members to use a preset", async () => {
  show({ policy: { ...policy, mode: "trusted" } }); fireEvent.click(screen.getByRole("button", { name: "Edit resource access" }))
  await screen.findByLabelText("Access mode")
  expect(screen.queryByRole("combobox", { name: "Ordered preset" })).toBeNull()
  expect(fetchMock.mock.calls.some(([url]) => url.includes("/access/presets"))).toBe(false)
})

it("permits a fresh reviewed preview after explicit reload even when member revision is unchanged", async () => {
  show({ previewStatus: 409 }); await choose()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  await screen.findByRole("alert")
  fireEvent.click(screen.getByRole("button", { name: "Reload current policy" }))
  await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => url.endsWith("/access?workspace_id=workspace"))).toHaveLength(2))
  await waitFor(() => expect(screen.getByLabelText("Resource")).toHaveValue(""))
  await choose()
  expect(screen.getByRole("button", { name: "Preview preset" })).toBeEnabled()
  expect(posts()).toHaveLength(1)
})


it.each([401, 403, 404])("invalidates reviewed preset consent after an acknowledged policy denial %s", async status => {
  const options: Options = {}
  show(options); await choose()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  fireEvent.click(await screen.findByRole("checkbox", { name: "I reviewed these exact changes and consequences" }))
  expect(screen.getByRole("button", { name: "Apply confirmed preset" })).toBeEnabled()
  options.policyReadStatus = status
  fireEvent.click(screen.getByRole("button", { name: "Reload current policy" }))
  await screen.findByText("Resource policy unavailable.")
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  expect(screen.queryByRole("button", { name: "Apply confirmed preset" })).toBeNull()
  expect(posts()).toHaveLength(1)
  options.policyReadStatus = undefined
  fireEvent.click(screen.getByRole("button", { name: "Reload policy" }))
  await choose()
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  expect(screen.queryByRole("button", { name: "Apply confirmed preset" })).toBeNull()
  expect(posts()).toHaveLength(1)
})

it("does not restore a late preview after an acknowledged audience loss", async () => {
  const options: Options = {}, client = show(options)
  await choose()
  const originalFetch = fetchMock.getMockImplementation()!
  let finishPreview: (() => void) | undefined
  fetchMock.mockImplementation((url: string, init?: RequestInit) => url.includes("/presets/preview")
    ? new Promise(resolve => { finishPreview = () => resolve(originalFetch(url, init)) })
    : originalFetch(url, init))
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  await waitFor(() => expect(finishPreview).toBeDefined())
  options.policyReadStatus = 403
  await act(async () => { await client.refetchQueries({ queryKey: ["member-resource-access", "workspace", "member"] }) })
  await screen.findByText("Resource policy unavailable.")
  await act(async () => { finishPreview!(); await Promise.resolve() })
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  expect(screen.queryByRole("button", { name: "Apply confirmed preset" })).toBeNull()
  expect(fetchMock.mock.calls.filter(([url, init]) => url.includes("/presets/apply") && init?.method === "POST")).toHaveLength(0)
})

it("preserves a manual draft when policy reload has a temporary failure", async () => {
  const right = { kind: "project" as const, id: "project", operation: "read" }
  const options: Options = { policy: { ...policy, rights: [right] } }
  show(options); await choose()
  fireEvent.click(screen.getByRole("button", { name: "Remove project project read" }))
  options.policyReadStatus = 500
  fireEvent.click(screen.getByRole("button", { name: "Reload current policy" }))
  await screen.findByText("Resource policy unavailable.")
  expect(screen.getByRole("region", { name: "Unsaved changes" })).toBeVisible()
  expect(screen.queryByRole("button", { name: "Remove project project read" })).toBeNull()
  expect(screen.getByRole("combobox", { name: "Ordered preset" })).toBeDisabled()
})


it.each([[401, 500], [403, 429], [404, 500]])("keeps consent revoked after policy denial %s followed by transient %s", async (denial, transient) => {
  const options: Options = {}, client = show(options)
  await choose()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  fireEvent.click(await screen.findByRole("checkbox", { name: "I reviewed these exact changes and consequences" }))
  options.policyReadStatus = denial
  fireEvent.click(screen.getByRole("button", { name: "Reload current policy" }))
  await screen.findByText("Resource policy unavailable.")
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  const key = ["member-resource-access", "workspace", "member"]
  const deniedError = client.getQueryState(key)?.error
  options.policyReadStatus = transient
  fireEvent.click(screen.getByRole("button", { name: "Reload policy" }))
  await waitFor(() => expect(client.getQueryState(key)?.error).not.toBe(deniedError))
  expect(screen.queryByLabelText("Access mode")).toBeNull()
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  expect(screen.queryByRole("button", { name: "Apply confirmed preset" })).toBeNull()
  expect(posts()).toHaveLength(1)
})

it.each([false, true])("ignores a late successful apply after audience loss (fresh policy recovered: %s)", async recovered => {
  const options: Options = {}, client = show(options)
  await choose()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  fireEvent.click(await screen.findByRole("checkbox", { name: "I reviewed these exact changes and consequences" }))
  const originalFetch = fetchMock.getMockImplementation()!
  let finishApply: (() => Promise<void>) | undefined
  fetchMock.mockImplementation((url: string, init?: RequestInit) => url.includes("/presets/apply")
    ? new Promise(resolve => { finishApply = async () => { resolve(await originalFetch(url, init)) } })
    : originalFetch(url, init))
  fireEvent.click(screen.getByRole("button", { name: "Apply confirmed preset" }))
  await waitFor(() => expect(finishApply).toBeDefined())
  const key = ["member-resource-access", "workspace", "member"]
  options.policyReadStatus = 403
  await act(async () => { await client.refetchQueries({ queryKey: key }) })
  await screen.findByText("Resource policy unavailable.")
  expect(screen.queryByLabelText("Access mode")).toBeNull()
  if (recovered) {
    options.policyReadStatus = undefined
    options.policyReadResponse = { ...policy, revision: 11, rights: [{ kind: "routine", id: "newer-routine", operation: "view" }] }
    fireEvent.click(screen.getByRole("button", { name: "Reload policy" }))
    await waitFor(() => expect(client.getQueryData<ResourcePolicy>(key)?.revision).toBe(11))
    await screen.findByLabelText("Access mode")
  }
  await act(async () => { await finishApply!() })
  expect(success).not.toHaveBeenCalled()
  expect(client.getQueryData<ResourcePolicy>(key)?.revision).not.toBe(9)
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  expect(screen.queryByRole("button", { name: "Apply confirmed preset" })).toBeNull()
  if (recovered) {
    expect(client.getQueryData<ResourcePolicy>(key)?.revision).toBe(11)
    expect(client.getQueryData<ResourcePolicy>(key)?.rights).toEqual(options.policyReadResponse!.rights)
  } else expect(screen.queryByLabelText("Access mode")).toBeNull()
})


it.each(["pending", "temporary-error"])("requires a fresh member read after A→B→A before late apply (%s)", async readState => {
  const client = show()
  await choose()
  fireEvent.click(screen.getByRole("button", { name: "Preview preset" }))
  fireEvent.click(await screen.findByRole("checkbox", { name: "I reviewed these exact changes and consequences" }))
  const originalFetch = fetchMock.getMockImplementation()!
  const reads: { memberId: string; resolve: (value: ReturnType<typeof response>) => void }[] = []
  let finishApply: (() => Promise<void>) | undefined
  fetchMock.mockImplementation((url: string, init?: RequestInit) => {
    if (url.includes("/presets/apply")) return new Promise(resolve => {
      finishApply = async () => { resolve(await originalFetch(url, init)) }
    })
    const member = /\/members\/(member|member-b)\/access\?/.exec(url)
    if (member && !init?.method) return new Promise<ReturnType<typeof response>>(resolve => {
      reads.push({ memberId: member[1], resolve })
    })
    return originalFetch(url, init)
  })
  fireEvent.click(screen.getByRole("button", { name: "Apply confirmed preset" }))
  await waitFor(() => expect(finishApply).toBeDefined())
  client.rerenderMember("member-b")
  await waitFor(() => expect(reads.some(read => read.memberId === "member-b")).toBe(true))
  client.rerenderMember("member")
  await waitFor(() => expect(reads.some(read => read.memberId === "member")).toBe(true))
  const firstA = reads.find(read => read.memberId === "member")!
  if (readState === "temporary-error") {
    await act(async () => { firstA.resolve(response({}, 500)) })
    await screen.findByText("Resource policy unavailable.")
  }
  await act(async () => { await finishApply!() })
  expect(success).not.toHaveBeenCalled()
  const key = ["member-resource-access", "workspace", "member"]
  expect(client.getQueryData<ResourcePolicy>(key)?.revision).not.toBe(9)
  expect(screen.queryByLabelText("Access mode")).toBeNull()
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  expect(screen.queryByRole("button", { name: "Apply confirmed preset" })).toBeNull()
  let recovery: Promise<void> | undefined
  if (readState === "temporary-error") {
    recovery = client.refetchQueries({ queryKey: key })
    await waitFor(() => expect(reads.filter(read => read.memberId === "member")).toHaveLength(2))
  }
  const freshA = reads.filter(read => read.memberId === "member").at(-1)!
  await act(async () => {
    freshA.resolve(response({ ...policy, revision: 15 }))
    reads.find(read => read.memberId === "member-b")!.resolve(response({ ...policy, membership_id: "member-b", revision: 13 }))
    await recovery
  })
  await screen.findByLabelText("Access mode")
  expect(client.getQueryData<ResourcePolicy>(key)?.revision).toBe(15)
  expect(screen.queryByLabelText("Preset consequences")).toBeNull()
  expect(posts()).toHaveLength(2)
})
