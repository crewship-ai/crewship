import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ResourceShareDialog } from "../resource-share-dialog"
import { resourceShareReplacement, resourcePolicySchema, type ResourcePolicy } from "@/lib/resource-sharing"

const h = vi.hoisted(() => ({ fetch: vi.fn(), role: "OWNER", mode: "trusted", loading: false }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, loading: h.loading }) }))
vi.mock("@/hooks/use-access-mode", () => ({ useAccessMode: () => h.mode }))
const resource = { kind: "routine" as const, id: "routine-1", name: "Invoice intake" }
const policy: ResourcePolicy = {
  membership_id: "member-1", revision: 7, mode: "restricted",
  rights: [{ kind: "agent", id: "agent-1", operation: "run" }, { kind: "routine", id: "routine-1", operation: "run" }],
  grant_details: [{ kind: "routine", id: "routine-1", operation: "run", issuer_id: "former-admin", issuer_name: "Alice", created_at: "2026-10-14T00:00:00Z", live: false }],
}
const roster = [
  { id: "owner-member", role: "OWNER", access_mode: "trusted", user: { id: "owner", full_name: "Owner", email: "owner@example.test" } },
  { id: "member-1", role: "VIEWER", access_mode: "restricted", user: { id: "guest", full_name: "Client One", email: "guest@example.test" } },
]
function response(body: unknown, status = 200) { return { ok: status < 400, status, json: async () => body } }
function mockApi(writeStatus = 200) {
  h.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
	if (url.includes("/guest-intake")) return response({project_id:"routine-1",crew_id:"",crew_name:"",revision:0})
	if (url.includes("/crews?")) return response([])
    if (init?.method === "PUT") return response({ ...JSON.parse(String(init.body)), revision: 8 }, writeStatus)
    if (url.includes("/members/owner-member/access?")) return response({...policy,membership_id:"owner-member",mode:"trusted",rights:[],grant_details:[]})
    return response(url.includes("/access?") ? policy : roster)
  })
}
function show(kind: "routine" | "project" | "crew" = "routine") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  render(<QueryClientProvider client={client}><ResourceShareDialog workspaceId="workspace-1" resource={{ ...resource, kind }} /></QueryClientProvider>)
  return client
}
const writes = () => h.fetch.mock.calls.filter(([, init]) => init?.method === "PUT")
afterEach(() => { cleanup(); h.fetch.mockReset(); h.role = "OWNER"; h.mode = "trusted"; h.loading = false })

describe("resource Share", () => {
  it.each([ ["VIEWER", "trusted", false], ["ADMIN", "restricted", false], ["ADMIN", "loading", false], ["ADMIN", "trusted", true] ])("does not offer Share or fetch for role %s / mode %s / loading %s", (role, mode, loading) => {
    h.role = role as string; h.mode = mode as string; h.loading = loading as boolean
    show(); expect(screen.queryByRole("button", { name: "Share" })).toBeNull(); expect(h.fetch).not.toHaveBeenCalled()
  })
  it("opens from the object, shows inactive provenance and never loads trusted member policies", async () => {
    mockApi(); show(); expect(h.fetch).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(await screen.findByText(/Inactive — current share authority is unavailable/)).toHaveTextContent("Alice")
    expect(screen.getByRole("dialog")).toHaveAccessibleName("Share Invoice intake")
    expect(screen.getByText(/also requires access to every agent/)).toBeTruthy()
    expect(h.fetch.mock.calls.some(([url]) => url.includes("/members/owner-member/access"))).toBe(false)
    expect(screen.getByRole("button", { name: "Save access for Client One" })).toBeDisabled()
  })
  it("revokes only this routine, keeps agent grants and sends no forged provenance", async () => {
    mockApi(); const client = show()
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Run"))
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0][1].body))).toEqual({ membership_id: "member-1", revision: 7, mode: "restricted", rights: [policy.rights[0]] })
    await waitFor(() => expect(client.getQueryData<ResourcePolicy>(["member-resource-access", "workspace-1", "member-1"])?.revision).toBe(8))
  })
  it("reissues inactive rights only with explicit consent", async () => {
    mockApi(); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Reissue run as me"))
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0][1].body)).reissue).toEqual([policy.rights[1]])
  })
  it("keeps a failed edit and permits explicit retry", async () => {
    mockApi(500); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Run")); fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Your edits are kept")
    expect(screen.getByLabelText("Run")).not.toBeChecked()
    expect(screen.getByRole("button", { name: "Save access for Client One" })).toBeEnabled()
  })
  it("blocks a stale save without retry and reloads the current revision explicitly", async () => {
    mockApi(409); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Run")); fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Access changed")
    expect(writes()).toHaveLength(1); expect(screen.getByRole("button", { name: "Save access for Client One" })).toBeDisabled()
    h.fetch.mockImplementation(async (url: string) => response(url.includes("/access?") ? { ...policy, revision: 9 } : roster))
    fireEvent.click(screen.getByRole("button", { name: "Reload shares" }))
    await waitFor(() => expect(screen.getByLabelText("Run")).toBeEnabled())
    expect(screen.getByLabelText("Run")).toBeChecked()
  })
  it("shows a policy read failure instead of an empty sharing list", async () => {
    h.fetch.mockImplementation(async (url: string) => response(url.includes("/access?") ? {} : roster, url.includes("/access?") ? 404 : 200))
    show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load all shares")
    expect(screen.queryByRole("button", { name: "Save access for Client One" })).toBeNull()
  })
  it("keeps independent project list/read rights and unrelated inactive rights unchanged", async () => {
    mockApi(); show("project"); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("List in Issues"))
    expect(screen.getByLabelText("Read issues")).not.toBeChecked()
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0][1].body))).toEqual({ membership_id: "member-1", revision: 7, mode: "restricted", rights: [...policy.rights, { kind: "project", id: "routine-1", operation: "list" }] })
  })
  it("labels routine draft editing explicitly and preserves independent Run authority", async () => {
    mockApi(); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    fireEvent.click(await screen.findByLabelText("Edit draft"))
    expect(screen.queryByLabelText("Read issues")).toBeNull()
    expect(screen.getByLabelText("Run")).toBeChecked()
    fireEvent.click(screen.getByLabelText("View definition"))
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String(writes()[0][1].body)).rights).toEqual(expect.arrayContaining([
      policy.rights[0], policy.rights[1], { kind: "routine", id: "routine-1", operation: "edit" }, { kind: "routine", id: "routine-1", operation: "view" },
    ]))
  })
  it("preserves unknown operations and refuses account conversion or invalid reissue", () => {
    const extended = { ...policy, rights: [...policy.rights, { kind: "routine" as const, id: "routine-1", operation: "future-operation" }] }
    expect(resourceShareReplacement(extended, resource, [], []).rights).toEqual([policy.rights[0], extended.rights[2]])
    expect(() => resourceShareReplacement({ ...policy, mode: "trusted" }, resource, [], [])).toThrow("Trusted access")
    expect(() => resourceShareReplacement(policy, resource, [], ["run"])).toThrow("Invalid resource operation")
    expect(() => resourceShareReplacement(policy, resource, ["unsupported-operation"], [])).toThrow("Invalid resource operation")
  })
  it("requires Edit for Publish and adds neither View nor Run implicitly", async () => {
    mockApi(); show(); fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(await screen.findByLabelText("Publish")).toBeDisabled()
    fireEvent.click(screen.getByLabelText("Run"))
    fireEvent.click(screen.getByLabelText("Edit draft"))
    fireEvent.click(screen.getByLabelText("Publish"))
    expect(screen.getByLabelText("View definition")).not.toBeChecked()
    expect(screen.getByLabelText("Run")).not.toBeChecked()
    fireEvent.click(screen.getByLabelText("Edit draft"))
    expect(screen.getByRole("button", { name: "Save access for Client One" })).toBeDisabled()
    expect(screen.getByRole("alert")).toHaveTextContent("Publish requires Edit")
    expect(() => resourceShareReplacement(policy, resource, ["publish"], [])).toThrow("Publish requires Edit")
    fireEvent.click(screen.getByLabelText("Edit draft"))
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const rights = JSON.parse(String(writes()[0][1].body)).rights
    expect(rights).toHaveLength(3)
    expect(rights).toEqual(expect.arrayContaining([policy.rights[0], { kind: "routine", id: "routine-1", operation: "edit" }, { kind: "routine", id: "routine-1", operation: "publish" }]))
  })
})


it("preserves a saved crew share when editing an unrelated routine", () => {
  const crew = {kind:"crew" as const,id:"crew-1",operation:"use"}
  const parsed = resourcePolicySchema.parse({...policy, rights:[...policy.rights,crew]})
  const changed = resourceShareReplacement(parsed,resource,["view"],[])
  expect(changed.rights).toContainEqual(crew)
  expect(changed.rights).toContainEqual(policy.rights[0])
})


describe("explicit crew Share", () => {
  it("shares crew Use with explicit dynamic-audience consent and preserves unrelated rights", async () => {
    mockApi(); show("crew")
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(await screen.findByText(/current and future agents/)).toBeTruthy()
    expect(screen.getByText(/never grants Delegate/)).toBeTruthy()
    fireEvent.click(await screen.findByLabelText("Use"))
    fireEvent.click(screen.getByRole("button", { name: "Save access for Client One" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const body = JSON.parse(writes()[0][1].body)
    expect(body.mode).toBe("restricted")
    expect(body.revision).toBe(7)
    expect(body.rights).toEqual(expect.arrayContaining([
      {kind:"crew",id:"routine-1",operation:"use"},
      {kind:"agent",id:"agent-1",operation:"run"},
      {kind:"routine",id:"routine-1",operation:"run"},
    ]))
    expect(body.grant_details).toBeUndefined()
    expect(body.rights.some((right: {kind:string;operation:string}) => right.kind==="crew" && ["manage","delegate"].includes(right.operation))).toBe(false)
  })

  it("does not turn crew sharing into a project intake or publication UI", async () => {
    mockApi(); show("crew")
    fireEvent.click(screen.getByRole("button", { name: "Share" }))
    expect(await screen.findByLabelText("View")).toBeTruthy()
    expect(screen.getByLabelText("Use")).toBeTruthy()
    expect(screen.queryByLabelText("Manage")).toBeNull()
    expect(screen.queryByLabelText("Publish")).toBeNull()
    expect(h.fetch.mock.calls.some(([url])=>url.includes("guest-intake"))).toBe(false)
  })
})


describe("trusted crew Manage sharing", () => {
  function trustedApi(role = "MEMBER", saved = false) {
    const person = {...roster[1], role, access_mode:"trusted"};
    const trusted:ResourcePolicy = {...policy, mode:"trusted", rights:[{kind:"crew",id:"other-crew",operation:"manage"}, ...(saved ? [{kind:"crew" as const,id:"routine-1",operation:"manage"}] : [])], grant_details:saved ? [{kind:"crew",id:"routine-1",operation:"manage",issuer_id:"old-admin",created_at:"old-time",live:false}] : []};
    h.fetch.mockImplementation(async (url:string,init?:RequestInit) => {
      if(init?.method === "PUT") return response({...JSON.parse(String(init.body)),revision:8});
      return response(url.includes("/access?") ? {...trusted,membership_id:url.includes("owner-member")?"owner-member":"member-1",rights:url.includes("owner-member")?[]:trusted.rights} : [roster[0],person]);
    });
    return trusted;
  }
  it("creates only trusted crew Manage and preserves mode and other shares",async()=>{
    trustedApi();show("crew");fireEvent.click(screen.getByRole("button",{name:"Share"}));
    const field=await screen.findByRole("group",{name:"Client One"});
    const {within}=await import("@testing-library/react");
    expect(within(field).queryByLabelText("Use")).toBeNull();
    fireEvent.click(within(field).getByLabelText("Manage crew"));
    fireEvent.click(within(field).getByRole("button",{name:"Save access for Client One"}));
    await waitFor(()=>expect(writes()).toHaveLength(1));
    expect(JSON.parse(String(writes()[0][1].body))).toEqual({membership_id:"member-1",revision:7,mode:"trusted",rights:[{kind:"crew",id:"other-crew",operation:"manage"},{kind:"crew",id:"routine-1",operation:"manage"}]});
  });
  it.each(["VIEWER","ADMIN","OWNER"])("shows retained inactive shares for %s, permits revoke and denies new/reissue",async(role)=>{
    trustedApi(role,true);show("crew");fireEvent.click(screen.getByRole("button",{name:"Share"}));
    const field=await screen.findByRole("group",{name:"Client One"});
    const {within}=await import("@testing-library/react");
    expect(within(field).getByText(/old-admin/)).toBeTruthy();
    expect(within(field).queryByLabelText("Reissue manage as me")).toBeNull();
    fireEvent.click(within(field).getByLabelText("Manage crew"));
    expect(within(field).getByLabelText("Manage crew")).toBeDisabled();
    fireEvent.click(within(field).getByRole("button",{name:"Save access for Client One"}));
    await waitFor(()=>expect(writes()).toHaveLength(1));
    expect(JSON.parse(String(writes()[0][1].body)).rights).toEqual([{kind:"crew",id:"other-crew",operation:"manage"}]);
  });
  it("rejects trusted View/Use and keeps Guest Manage invalid in the replacement helper",()=>{
    const crew={kind:"crew" as const,id:"crew-1",name:"Crew"};
    expect(resourceShareReplacement({...policy,mode:"trusted"},crew,["manage"],[]).mode).toBe("trusted");
    expect(()=>resourceShareReplacement({...policy,mode:"trusted"},crew,["use"],[])).toThrow("Invalid resource operation");
    expect(()=>resourceShareReplacement(policy,crew,["manage"],[])).toThrow("Invalid resource operation");
  });
});
