import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { MemberResourceAccess } from "../member-resource-access"
import { PageSaveBar, PageSaveProvider } from "@/components/ui/page-save-bar"

/**
 * Reach, the third card of a person's sheet: the whole workspace, or a Guest's
 * shares. The editor loads the saved policy when the card shows; every edit is
 * a draft that waits for the page's Save. Operations are named for what they
 * let someone do ("Chat with it"), never by their ids.
 */

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
const toastError = vi.hoisted(() => vi.fn())
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: toastError } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
const policy = { membership_id: "membership-1", revision: 7, mode: "restricted", rights: [{ kind: "agent", id: "agent-1", operation: "chat" }] }
function response(body: unknown, status = 200) { return { ok: status < 400, status, json: async () => body } }
function show(role = "MEMBER", onSaved?: () => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  // Inside the page's save bar, as Settings › Members renders it.
  render(<QueryClientProvider client={client}><PageSaveProvider>
    <MemberResourceAccess workspaceId="workspace-1" memberId="membership-1" label="Client One" role={role} onSaved={onSaved} />
    <PageSaveBar />
  </PageSaveProvider></QueryClientProvider>)
}
const bar = () => screen.queryByRole("region", { name: "Unsaved changes" })
const saveButton = () => screen.getByRole("button", { name: "Save" })
const puts = () => fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")
const sent = () => JSON.parse(puts()[0][1].body)
/** Opens the "Share something" form. */
const share = async () => { fireEvent.click(await screen.findByRole("button", { name: "Share something" })) }
const stop = (re: RegExp) => screen.findByRole("button", { name: re })
function mockPolicy() {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 })
    if (url.includes("/access/effective")) return response({ membership_id: policy.membership_id, revision: 7, explanation: "", account_restricted: true, decisions: [] })
    if (url.includes("/members/")) return response(policy)
    return response([{ id: "agent-1", name: "Shared agent" }, { id: "agent-2", name: "Other agent" }])
  })
}
afterEach(() => { cleanup(); fetchMock.mockReset(); toastError.mockReset() })

describe("member resource access", () => {
  it("shows trusted crew management shares and requires an explicit saved revoke before Guest conversion", async () => {
    const trusted = { ...policy, mode: "trusted", rights: [{ kind: "crew", id: "crew-1", operation: "manage" }] }
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 })
      return response(trusted)
    })
    show()
    const remove = await stop(/^Stop sharing .*: Manage it$/)
    // Why Guest is not offered: said, not just greyed out.
    expect(screen.getByRole("radio", { name: "Guest" })).toBeDisabled()
    expect(screen.getByText(/Remove the crew shares below and save/)).toBeInTheDocument()
    expect(bar()).toBeNull()
    fireEvent.click(remove)
    expect(bar()).toHaveTextContent("1 unsaved change")
    fireEvent.click(saveButton())
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(sent()).toEqual({ ...trusted, rights: [] })
    await waitFor(() => expect(screen.getByRole("radio", { name: "Guest" })).toBeEnabled())
  })

  it("reissues a trusted crew share without clearing it or changing reach", async () => {
    const right = { kind: "crew", id: "crew-1", operation: "manage" }
    const trusted = { ...policy, mode: "trusted", rights: [right], grant_details: [{ ...right, issuer_id: "old-admin", created_at: "original", live: false }] }
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 })
      if (url.includes("/access/effective")) return response({ membership_id: trusted.membership_id, revision: 7, explanation: "Crew shares", account_restricted: false, decisions: [{ ...right, state: "denied", reason: "Current crew share authority is unavailable." }] })
      return response(trusted)
    })
    show()
    // "What they can reach right now" names the inactive grant and its reason.
    const row = (await screen.findByText(/Current crew share authority is unavailable/)).closest("li")!
    expect(within(row).getByText("Inactive")).toBeInTheDocument()
    fireEvent.click(within(row).getByRole("button", { name: "Share it as me" }))
    fireEvent.click(saveButton())
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(sent()).toEqual({ membership_id: trusted.membership_id, revision: 7, mode: "trusted", rights: [right], reissue: [right] })
  })

  it("requires an explicit Edit grant before saving Publish without adding View or Run", async () => {
    mockPolicy(); show(); await share()
    fireEvent.change(await screen.findByLabelText("What"), { target: { value: "routine" } })
    await screen.findByRole("option", { name: "Other agent" })
    fireEvent.change(screen.getByLabelText("Which one"), { target: { value: "agent-2" } })
    fireEvent.change(screen.getByLabelText("They may"), { target: { value: "publish" } })
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(screen.getByText(/Publish requires Edit on the same routine/)).toBeVisible()
    expect(saveButton()).toBeDisabled()
    expect(puts()).toHaveLength(0)
    fireEvent.change(screen.getByLabelText("They may"), { target: { value: "edit" } })
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(saveButton()).toBeEnabled()
    fireEvent.click(saveButton())
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(sent().rights).toEqual([...policy.rights, { kind: "routine", id: "agent-2", operation: "publish" }, { kind: "routine", id: "agent-2", operation: "edit" }])
  })

  it("loads only the selected membership and saves explicit deny-all with the original revision", async () => {
    mockPolicy(); show()
    fireEvent.click(await stop(/^Stop sharing .*: Chat with it$/))
    // The card has no Save of its own: the page bar counts the removed grant.
    expect(bar()).toHaveTextContent("1 unsaved change")
    fireEvent.click(saveButton())
    await waitFor(() => expect(puts()).toHaveLength(1))
    const [url] = puts()[0]
    expect(url).toContain("/members/membership-1/access?workspace_id=workspace-1")
    expect(sent()).toEqual({ ...policy, rights: [] })
    // The saved revision is the new baseline: nothing left pending.
    await waitFor(() => expect(bar()).not.toHaveTextContent("unsaved"))
  })

  it("reloads the roster after a saved reach change, so its Guest marker follows (#2878)", async () => {
    const onSaved = vi.fn()
    mockPolicy(); show("MEMBER", onSaved)
    fireEvent.click(await screen.findByRole("radio", { name: "Whole workspace" }))
    fireEvent.click(saveButton())
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
  })

  it("does not reload the roster when only the shares changed", async () => {
    const onSaved = vi.fn()
    mockPolicy(); show("MEMBER", onSaved)
    fireEvent.click(await stop(/^Stop sharing .*: Chat with it$/))
    fireEvent.click(saveButton())
    await waitFor(() => expect(bar()).not.toHaveTextContent("unsaved"))
    expect(onSaved).not.toHaveBeenCalled()
  })

  it("shows no Save while nothing has been edited, and Discard restores the saved shares", async () => {
    mockPolicy(); show()
    fireEvent.click(await stop(/^Stop sharing .*: Chat with it$/))
    expect(bar()).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    expect(bar()).toBeNull()
    expect(screen.getByRole("button", { name: /^Stop sharing .*: Chat with it$/ })).toBeTruthy()
  })

  it("a failed write is a corner toast and keeps the draft", async () => {
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return response({}, 500)
      return response(url.includes("/members/") ? policy : [{ id: "agent-1", name: "Shared agent" }])
    })
    show()
    fireEvent.click(await stop(/^Stop sharing .*: Chat with it$/))
    fireEvent.click(saveButton())
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(toastError.mock.calls[0][0]).toBe("Couldn’t save Reach for Client One")
    expect(toastError.mock.calls[0][1]).toMatchObject({ description: "Resource access was not saved Your edits are kept." })
    expect(bar()).toHaveTextContent("1 unsaved change")
  })

  it("adds only an exact selected operation and prevents duplicate shares", async () => {
    mockPolicy(); show(); await share()
    await screen.findByRole("option", { name: "Other agent" })
    fireEvent.change(screen.getByLabelText("Which one"), { target: { value: "agent-2" } })
    fireEvent.change(screen.getByLabelText("They may"), { target: { value: "run" } })
    fireEvent.click(screen.getByRole("button", { name: "Share" })); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(bar()).toHaveTextContent("1 unsaved change")
    fireEvent.click(saveButton())
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(sent().rights).toEqual([...policy.rights, { kind: "agent", id: "agent-2", operation: "run" }])
  })

  it("keeps a rejected draft and requires explicit reload after a conflict without retrying the write", async () => {
    let current = policy
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") { current = { ...policy, revision: 9, rights: [] }; return response({}, 409) }
      if (url.includes("/access/effective")) return response({ membership_id: policy.membership_id, revision: current.revision, explanation: "", account_restricted: true, decisions: [] })
      return response(url.includes("/members/") ? current : [{ id: "agent-1", name: "Shared agent" }])
    })
    show(); await share()
    await screen.findByRole("option", { name: "Shared agent" })
    fireEvent.change(screen.getByLabelText("They may"), { target: { value: "run" } })
    fireEvent.change(screen.getByLabelText("Which one"), { target: { value: "agent-1" } })
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(saveButton())
    await screen.findByText("Policy changed. Reload to discard this draft and review the current grants.")
    await waitFor(() => expect(saveButton()).toBeDisabled())
    expect(screen.getByRole("button", { name: /^Stop sharing .*: Chat with it$/ })).toBeTruthy()
    expect(puts()).toHaveLength(1)
    fireEvent.click(screen.getByRole("button", { name: "Reload current policy" }))
    await screen.findByText("Nothing is shared with them.")
    // The reloaded policy is the new baseline; the stale draft is gone.
    await waitFor(() => expect(bar()?.textContent ?? "").not.toContain("unsaved"))
  })

  it("clears shares when explicitly restoring whole-workspace reach", async () => {
    mockPolicy(); show()
    fireEvent.click(await screen.findByRole("radio", { name: "Whole workspace" }))
    fireEvent.click(saveButton())
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(sent()).toEqual({ ...policy, mode: "trusted", rights: [] })
  })

  it("offers Whole workspace and Guest as a radio list, and never offers Guest to an admin", async () => {
    mockPolicy(); show("ADMIN")
    const reach = await screen.findByRole("radiogroup", { name: "Reach" })
    expect(within(reach).getByRole("radio", { name: "Guest" })).toHaveAttribute("aria-checked", "true")
    expect(within(reach).getByRole("radio", { name: "Whole workspace" })).toHaveAttribute("aria-checked", "false")
    cleanup(); fetchMock.mockReset()
    fetchMock.mockImplementation(async (url: string) => url.includes("/access/effective")
      ? response({ membership_id: policy.membership_id, revision: 7, explanation: "", account_restricted: false, decisions: [] })
      : response({ ...policy, mode: "trusted", rights: [] }))
    show("ADMIN")
    expect(await screen.findByRole("radio", { name: "Guest" })).toBeDisabled()
    expect(screen.getByText("Owners and admins always reach the whole workspace.")).toBeInTheDocument()
  })

  it("shows names for agent and project grants whichever resource kind is selected (#2863)", async () => {
    const mixed = { ...policy, rights: [{ kind: "agent", id: "agent-1", operation: "chat" }, { kind: "project", id: "project-1", operation: "read" }] }
    fetchMock.mockImplementation(async (url: string) => {
      if (url.includes("/access/effective")) return response({ membership_id: policy.membership_id, revision: 7, explanation: "", account_restricted: true, decisions: [] })
      if (url.includes("/members/")) return response(mixed)
      if (url.startsWith("/api/v1/projects")) return response([{ id: "project-1", name: "Roadmap project" }])
      return response([{ id: "agent-1", name: "Shared agent" }])
    })
    show()
    const grants = await screen.findByRole("list", { name: "Resource grants" })
    await waitFor(() => expect(grants.textContent).toContain("Agent · Shared agent"))
    await waitFor(() => expect(grants.textContent).toContain("Project · Roadmap project"))
    expect(grants.textContent).toContain("Chat with it")
    expect(grants.textContent).toContain("Read it")
    await share()
    fireEvent.change(screen.getByLabelText("What"), { target: { value: "project" } })
    await screen.findByRole("option", { name: "Roadmap project" })
    expect(grants.textContent).toContain("Agent · Shared agent")
    expect(grants.textContent).toContain("Project · Roadmap project")
  })

  it("never shows an operation id: every operation of every kind has a name", async () => {
    mockPolicy(); show(); await share()
    const seen: string[] = []
    for (const kind of ["crew", "agent", "project", "routine"]) {
      fireEvent.change(await screen.findByLabelText("What"), { target: { value: kind } })
      for (const option of (screen.getByLabelText("They may") as HTMLSelectElement).options) seen.push(option.textContent ?? "")
    }
    expect(seen).toContain("Find it")
    expect(seen).toContain("Delete its issues")
    expect(seen.filter((label) => /[_.]|^(discover|delete_issues|contribute|results)$/.test(label))).toEqual([])
  })

  it("shares one selected routine and explains its agent dependency", async () => {
    mockPolicy(); show(); await share()
    await screen.findByLabelText("What")
    fireEvent.change(screen.getByLabelText("What"), { target: { value: "routine" } })
    fireEvent.change(screen.getByLabelText("They may"), { target: { value: "run" } })
    await screen.findByRole("option", { name: "Other agent" })
    fireEvent.change(screen.getByLabelText("Which one"), { target: { value: "agent-2" } })
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(screen.getByText(/Guest restrictions apply to the entire account/)).toHaveTextContent("run access to every agent")
    fireEvent.click(saveButton())
    await waitFor(() => expect(puts()).toHaveLength(1))
    expect(sent().rights).toContainEqual({ kind: "routine", id: "agent-2", operation: "run" })
    expect(fetchMock.mock.calls.some(([url]) => url.includes("/workspaces/workspace-1/pipelines"))).toBe(true)
  })

  it("rejects a replacement membership and provides no save control", async () => {
    fetchMock.mockResolvedValue(response({ ...policy, membership_id: "new-membership" }))
    show()
    await screen.findByRole("alert")
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull()
  })
})

it("keeps inactive issuer metadata visible and reissues only after an explicit choice", async () => {
  const inactive = { ...policy, grant_details: [{ ...policy.rights[0], issuer_id: "former-admin", issuer_name: "Former administrator", created_at: "2026-10-14T00:00:00Z", live: false }] }
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return response({ ...inactive, revision: 8 })
    if (url.includes("/access/effective")) return response({ membership_id: policy.membership_id, revision: 7, explanation: "", account_restricted: true, decisions: [{ ...policy.rights[0], state: "denied", reason: "The grant issuer no longer has active trusted administrator authority in this workspace." }] })
    if (url.includes("/members/")) return response(inactive)
    return response([{ id: "agent-1", name: "Shared agent" }])
  })
  show()
  expect(await screen.findByText(/Former administrator/)).toHaveTextContent(/inactive/)
  expect(bar()).toBeNull()
  fireEvent.click(await screen.findByRole("button", { name: "Share it as me" }))
  expect(bar()).toHaveTextContent("1 unsaved change")
  fireEvent.click(saveButton())
  await waitFor(() => expect(puts()).toHaveLength(1))
  expect(sent()).toEqual({ ...policy, reissue: policy.rights })
})

it("still offers Share it as me when the effective check is unavailable", async () => {
  const inactive = { ...policy, grant_details: [{ ...policy.rights[0], issuer_id: "former-admin", created_at: "old", live: false }] }
  fetchMock.mockImplementation(async (url: string) => (url.includes("/access/effective") ? response({}, 500) : response(url.includes("/members/") ? inactive : [{ id: "agent-1", name: "Shared agent" }])))
  show()
  expect(await screen.findByRole("button", { name: "Share it as me" })).toBeInTheDocument()
})

it("keeps crew shares visible and preserves them while editing another resource", async () => {
  const crew = { kind: "crew", id: "crew-1", operation: "use" }
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 })
    if (url.includes("/access/effective")) return response({ membership_id: policy.membership_id, revision: 7, account_restricted: true, explanation: "Crew inheritance", decisions: [] })
    if (url.includes("/members/")) return response({ ...policy, rights: [...policy.rights, crew] })
    return response([{ id: "agent-1", name: "Shared agent" }])
  })
  show()
  expect(await stop(/^Stop sharing .*: Use it$/)).toBeVisible()
  fireEvent.click(await stop(/^Stop sharing .*: Chat with it$/))
  fireEvent.click(saveButton())
  await waitFor(() => expect(puts()).toHaveLength(1))
  expect(sent().rights).toEqual([crew])
})

it("adds an explicit crew Use share from the person editor without widening other rights", async () => {
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 })
    if (url.includes("/access/effective")) return response({ membership_id: policy.membership_id, revision: 7, account_restricted: true, explanation: "Current shares", decisions: [] })
    if (url.includes("/members/")) return response(policy)
    if (url.includes("/crews?")) return response([{ id: "crew-1", name: "Shared crew" }])
    return response([{ id: "agent-1", name: "Shared agent" }])
  })
  show(); await share()
  fireEvent.change(await screen.findByLabelText("What"), { target: { value: "crew" } })
  await screen.findByRole("option", { name: "Shared crew" })
  expect(screen.getByText(/current and future agents/)).toHaveTextContent("execution preflight")
  const operations = screen.getByLabelText("They may") as HTMLSelectElement
  expect([...operations.options].map(option => option.value)).toEqual(["view", "use"])
  fireEvent.change(screen.getByLabelText("Which one"), { target: { value: "crew-1" } })
  fireEvent.change(operations, { target: { value: "use" } })
  fireEvent.click(screen.getByRole("button", { name: "Share" }))
  fireEvent.click(saveButton())
  await waitFor(() => expect(puts()).toHaveLength(1))
  expect(sent()).toEqual({ ...policy, rights: [...policy.rights, { kind: "crew", id: "crew-1", operation: "use" }] })
})

it.each(["MEMBER", "MANAGER"])("creates trusted Manage for %s from the person card without clearing other shares", async role => {
  const trusted = { ...policy, mode: "trusted", rights: [{ kind: "crew", id: "crew-old", operation: "manage" }] }
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 })
    if (url.includes("/access/effective")) return response({ membership_id: trusted.membership_id, revision: 7, account_restricted: false, explanation: "Trusted", decisions: [] })
    if (url.includes("/members/")) return response(trusted)
    return response([{ id: "crew-new", name: "New managed crew" }])
  })
  show(role); await share()
  await screen.findByRole("option", { name: "New managed crew" })
  expect(screen.getByLabelText("What")).toHaveValue("crew")
  expect(screen.getByLabelText("They may")).toHaveValue("manage")
  fireEvent.change(screen.getByLabelText("Which one"), { target: { value: "crew-new" } })
  fireEvent.click(screen.getByRole("button", { name: "Share" })); fireEvent.click(saveButton())
  await waitFor(() => expect(puts()).toHaveLength(1))
  expect(sent()).toEqual({ membership_id: trusted.membership_id, revision: 7, mode: "trusted", rights: [...trusted.rights, { kind: "crew", id: "crew-new", operation: "manage" }] })
})

it.each(["VIEWER", "ADMIN", "OWNER"])("keeps trusted sharing and reissue unavailable to %s while showing saved shares", async role => {
  const right = { kind: "crew", id: "crew-old", operation: "manage" }
  fetchMock.mockImplementation(async (url: string) => (url.includes("/access/effective")
    ? response({ membership_id: policy.membership_id, revision: 7, account_restricted: false, explanation: "", decisions: [{ ...right, state: "denied", reason: "Current crew share authority is unavailable." }] })
    : response({ ...policy, mode: "trusted", rights: [right], grant_details: [{ ...right, issuer_id: "old-admin", created_at: "old", live: false }] })))
  show(role)
  await stop(/^Stop sharing .*: Manage it$/)
  expect(screen.queryByRole("button", { name: "Share something" })).toBeNull()
  expect(screen.queryByLabelText("What")).toBeNull()
  await screen.findByText(/Current crew share authority is unavailable/)
  expect(screen.queryByRole("button", { name: "Share it as me" })).toBeNull()
})
