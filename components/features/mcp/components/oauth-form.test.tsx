import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { OAuthForm } from "./oauth-form"
const state = vi.hoisted(() => ({ fetch: vi.fn(), success: vi.fn(), error: vi.fn(), info: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => state.fetch(...args) }))
vi.mock("sonner", () => ({ toast: state }))
const reply = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const provider = { auth_url: "https://provider.invalid/authorize", token_url: "https://provider.invalid/token", default_scopes: "read write" }
const props = () => ({ workspaceId: "workspace", envKey: "SERVICE_TOKEN", onAddCredential: vi.fn(), onSelectCredential: vi.fn(), onCancel: vi.fn() })
function deferred() { let resolve!: (value: Response) => void; const promise = new Promise<Response>(yes => { resolve = yes }); return { promise, resolve } }
async function flush() { await act(async () => { for (let i=0;i<8;i++) await Promise.resolve() }) }
async function fill() { await flush(); fireEvent.click(screen.getByRole("button", { name: "Google" })); fireEvent.change(screen.getByLabelText("Client ID"), { target: { value: " client " } }); fireEvent.change(screen.getByLabelText("Client Secret"), { target: { value: " fixture-input " } }) }
beforeEach(() => {
 vi.useFakeTimers(); state.fetch.mockReset(); state.success.mockReset(); state.error.mockReset(); state.info.mockReset()
 vi.spyOn(window,"open").mockReturnValue({ closed: false, close: vi.fn() } as unknown as Window)
 state.fetch.mockImplementation(async (input: string, init?: RequestInit) => {
  const url = new URL(input,"http://localhost")
  if (url.pathname.endsWith("/providers")) return reply({ google: provider })
  if (url.pathname === "/api/v1/credentials" && init?.method === "POST") return reply({ id: "credential", name: "saved", status: "PENDING" })
  if (url.pathname.endsWith("/loopback") || url.pathname.endsWith("/initiate")) return reply({ auth_url: "https://provider.invalid/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A9999%2Fcallback" })
  return reply({ status: "PENDING" })
 })
})
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
it("does not start authorization after the form unmounts during credential creation", async () => {
 const pending = deferred(); const p = props(); const view = render(<OAuthForm {...p} />); await fill(); state.fetch.mockReturnValueOnce(pending.promise); fireEvent.click(screen.getByRole("button", { name: "Authorize" })); view.unmount()
 await act(async () => pending.resolve(reply({ id: "old", status: "PENDING" }))); await flush()
 expect(p.onAddCredential).not.toHaveBeenCalled(); expect(window.open).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0)
})
it("stops old workspace polling and clears its provider credentials", async () => {
 const p = props(); const view = render(<OAuthForm {...p} />); await fill(); fireEvent.click(screen.getByRole("button", { name: "Authorize" })); await flush(); expect(window.open).toHaveBeenCalledOnce()
 view.rerender(<OAuthForm {...p} workspaceId="next" />); await flush(); const calls = state.fetch.mock.calls.length; await act(async () => vi.advanceTimersByTimeAsync(6000));
 expect(state.fetch.mock.calls.slice(calls).some(([url]) => String(url).includes("workspace_id=workspace"))).toBe(false)
 expect(screen.queryByDisplayValue(" fixture-input ")).not.toBeInTheDocument(); expect(p.onSelectCredential).not.toHaveBeenCalled()
})
it("ignores an in-flight successful poll after unmount", async () => {
 const p = props(); const view = render(<OAuthForm {...p} />); await fill(); fireEvent.click(screen.getByRole("button", { name: "Authorize" })); await flush(); const pending = deferred(); state.fetch.mockReturnValueOnce(pending.promise)
 await act(async () => vi.advanceTimersByTimeAsync(2000)); view.unmount(); await act(async () => pending.resolve(reply({ status: "ACTIVE" }))); await flush()
 expect(p.onSelectCredential).not.toHaveBeenCalled(); expect(state.success).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0)
})
async function authorize() { await fill(); fireEvent.click(screen.getByRole("button", { name: "Authorize" })); await flush() }
it("creates a trimmed credential, opens the provider and selects it once active", async () => {
 const p = props(); render(<OAuthForm {...p} />); await authorize()
 const call = state.fetch.mock.calls.find(([,init]) => init?.method === "POST")!; expect(JSON.parse(call[1].body)).toMatchObject({ type: "OAUTH2", scope: "WORKSPACE", oauth_client_id: "client", oauth_client_secret: "fixture-input", oauth_scopes: "read write" })
 const name = JSON.parse(call[1].body).name; state.fetch.mockResolvedValueOnce(reply({ status: "ACTIVE" })); await act(async () => vi.advanceTimersByTimeAsync(2000)); expect(p.onSelectCredential).toHaveBeenCalledExactlyOnceWith(name); expect(state.success).toHaveBeenCalledOnce(); expect(vi.getTimerCount()).toBe(0)
})
it.each(["http", "network", "pending"])("continues polling after %s and then succeeds", async outcome => {
 const p = props(); render(<OAuthForm {...p} />); await authorize()
 if(outcome === "network") state.fetch.mockRejectedValueOnce(new Error("offline")); else state.fetch.mockResolvedValueOnce(reply({ status: "PENDING" },outcome === "http" ? 503 : 200))
 await act(async () => vi.advanceTimersByTimeAsync(2000)); expect(p.onSelectCredential).not.toHaveBeenCalled(); state.fetch.mockResolvedValueOnce(reply({ status: "ACTIVE" })); await act(async () => vi.advanceTimersByTimeAsync(2000)); expect(p.onSelectCredential).toHaveBeenCalledOnce()
})
it("bounds authorization wait and closes the owned popup", async () => {
 render(<OAuthForm {...props()} />); await authorize(); const popup=vi.mocked(window.open).mock.results[0].value as Window
 await act(async () => vi.advanceTimersByTimeAsync(122000)); expect(state.error).toHaveBeenCalledWith("OAuth authorization timed out"); expect(popup.close).toHaveBeenCalledOnce(); expect(vi.getTimerCount()).toBe(0); expect(screen.getByRole("button",{name:"Authorize"})).toBeEnabled()
})
it("does not overlap slow polling requests", async () => {
 render(<OAuthForm {...props()} />); await authorize(); const pending=deferred(); state.fetch.mockReturnValueOnce(pending.promise); const calls=state.fetch.mock.calls.length
 await act(async () => vi.advanceTimersByTimeAsync(10000)); expect(state.fetch).toHaveBeenCalledTimes(calls+1); await act(async()=>pending.resolve(reply({status:"PENDING"}))); await act(async()=>vi.advanceTimersByTimeAsync(2000)); expect(state.fetch).toHaveBeenCalledTimes(calls+2)
})
it.each(["detail","fallback","malformed","network"])("reports credential creation %s failure without opening a popup",async outcome=>{
 render(<OAuthForm {...props()} />); await fill()
 if(outcome==="network")state.fetch.mockRejectedValueOnce(new Error("offline")); else state.fetch.mockResolvedValueOnce(outcome==="malformed"?new Response("broken",{status:500}):reply({error:outcome==="detail"?"Denied":{}},400))
 fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); expect(state.error).toHaveBeenCalledWith(outcome==="detail"?"Denied":outcome==="network"?"Network error during OAuth setup":"Failed to create OAuth credential"); expect(window.open).not.toHaveBeenCalled(); expect(screen.getByRole("button",{name:"Authorize"})).toBeEnabled()
})
it.each(["raw-code","https://provider.invalid/callback?code=returned-code","https://provider.invalid/no-code"])("exchanges manual response %s and stops polling",async input=>{
 const p=props(); render(<OAuthForm {...p} />); await authorize(); fireEvent.change(screen.getByLabelText("Manual authorization code or redirect URL"),{target:{value:` ${input} `}}); state.fetch.mockResolvedValueOnce(reply({})); fireEvent.click(screen.getByRole("button",{name:"Submit"})); await flush()
 const call=state.fetch.mock.calls.find(([url])=>String(url).includes("/exchange"))!; expect(JSON.parse(call[1].body)).toEqual({credential_id:"credential",code:input.includes("?code=")?"returned-code":input,redirect_uri:"http://localhost:9999/callback"}); expect(p.onSelectCredential).toHaveBeenCalledOnce(); expect(vi.getTimerCount()).toBe(0)
})
it.each(["detail","fallback","malformed","network"])("keeps manual response retryable after %s failure",async outcome=>{
 render(<OAuthForm {...props()} />); await authorize(); fireEvent.change(screen.getByLabelText("Manual authorization code or redirect URL"),{target:{value:"code"}})
 if(outcome==="network")state.fetch.mockRejectedValueOnce(new Error("offline")); else state.fetch.mockResolvedValueOnce(outcome==="malformed"?new Response("broken",{status:500}):reply({error:outcome==="detail"?"Denied":{}},400))
 fireEvent.click(screen.getByRole("button",{name:"Submit"})); await flush(); expect(state.error).toHaveBeenCalledWith(outcome==="detail"?"Denied":outcome==="network"?"Network error during code exchange":outcome==="malformed"?"Code exchange failed":"Failed to exchange code"); expect(screen.getByRole("button",{name:"Waiting for authorization..."})).toBeDisabled(); expect(screen.getByRole("button",{name:"Submit"})).toBeEnabled()
})
it("ignores a delayed manual response after workspace change",async()=>{
 const p=props(); const view=render(<OAuthForm {...p} />); await authorize(); fireEvent.change(screen.getByLabelText("Manual authorization code or redirect URL"),{target:{value:"code"}}); const pending=deferred(); state.fetch.mockReturnValueOnce(pending.promise); fireEvent.click(screen.getByRole("button",{name:"Submit"})); view.rerender(<OAuthForm {...p} workspaceId="next" />); await act(async()=>pending.resolve(reply({}))); expect(p.onSelectCredential).not.toHaveBeenCalled(); expect(state.success).not.toHaveBeenCalled()
})
it("reports a blocked popup without starting timers",async()=>{
 vi.mocked(window.open).mockReturnValue(null); render(<OAuthForm {...props()} />); await authorize(); expect(state.error).toHaveBeenCalledWith(expect.stringContaining("Popup blocked")); expect(vi.getTimerCount()).toBe(0)
})
it.each(["http","network"])("allows custom setup when provider discovery has a %s failure",async outcome=>{
 if(outcome==="network")state.fetch.mockRejectedValueOnce(new Error("offline"));else state.fetch.mockResolvedValueOnce(reply({},503)); render(<OAuthForm {...props()} />); await flush(); expect(screen.getByRole("button",{name:"Google"})).toBeDisabled(); fireEvent.click(screen.getByRole("button",{name:"Custom"})); expect(screen.getByLabelText("Auth URL")).toHaveValue(""); expect(screen.getByRole("button",{name:"Authorize"})).toBeDisabled()
})
it("submits custom endpoints and scopes and supports cancel",async()=>{
 const p={...props(),envKey:""}; render(<OAuthForm {...p} />); await flush(); fireEvent.click(screen.getByRole("button",{name:"Custom"})); for(const [label,value] of [["Client ID","client"],["Client Secret","fixture-input"],["Auth URL",provider.auth_url],["Token URL",provider.token_url],["Scopes","custom-scope"]])fireEvent.change(screen.getByLabelText(label),{target:{value}})
 fireEvent.click(screen.getByRole("button",{name:"Cancel"})); expect(p.onCancel).toHaveBeenCalledOnce(); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); const call=state.fetch.mock.calls.find(([,init])=>init?.method==="POST")!; expect(JSON.parse(call[1].body)).toMatchObject({name:expect.stringMatching(/^custom-oauth-/),oauth_auth_url:provider.auth_url,oauth_token_url:provider.token_url,oauth_scopes:"custom-scope"})
})
it.each(["https://public.example","http://10.0.0.5","http://172.16.0.8","http://192.168.0.5"])("uses the correct callback mechanism on %s",async origin=>{
 vi.stubGlobal("location",new URL(origin)); const p=props(); render(<OAuthForm {...p} />); await authorize(); const endpoint=origin.includes("public")?"/initiate":"/loopback"; const call=state.fetch.mock.calls.find(([url])=>String(url).includes(endpoint))!; expect(call).toBeDefined(); expect(JSON.parse(call[1].body)).toEqual(origin.includes("public")?{credential_id:"credential",redirect_uri:origin+"/api/v1/oauth/callback"}:{credential_id:"credential"}); expect(window.open).toHaveBeenCalledOnce(); if(!origin.includes("public"))expect(state.info).toHaveBeenCalledOnce()
})
it.each(["http://localhost","https://public.example","http://10.0.0.5"])("reports authorization-start failures on %s",async origin=>{
 vi.stubGlobal("location",new URL(origin)); render(<OAuthForm {...props()} />); await fill(); state.fetch.mockResolvedValueOnce(reply({id:"credential"})).mockResolvedValueOnce(reply({error:"Provider refused"},400)); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); expect(state.error).toHaveBeenCalledWith("Provider refused"); expect(window.open).not.toHaveBeenCalled(); expect(screen.getByRole("button",{name:"Authorize"})).toBeEnabled()
})
it.each(["http://localhost","https://public.example","http://10.0.0.5"])("handles a malformed authorization-start error on %s",async origin=>{
 vi.stubGlobal("location",new URL(origin)); render(<OAuthForm {...props()} />); await fill(); state.fetch.mockResolvedValueOnce(reply({id:"credential"})).mockResolvedValueOnce(new Response("broken",{status:500})); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); expect(state.error).toHaveBeenCalledWith(origin.includes("public")?"Failed to initiate OAuth":"Failed to start OAuth"); expect(window.open).not.toHaveBeenCalled()
})
it.each(["http://localhost","https://public.example","http://10.0.0.5"])("handles an unstructured authorization-start error on %s",async origin=>{
 vi.stubGlobal("location",new URL(origin)); render(<OAuthForm {...props()} />); await fill(); state.fetch.mockResolvedValueOnce(reply({id:"credential"})).mockResolvedValueOnce(reply({},500)); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); expect(state.error).toHaveBeenCalledWith(origin.includes("public")?"Failed to initiate OAuth flow":"Failed to start OAuth flow")
})
it.each(["http://localhost","http://10.0.0.5"])("handles provider URLs without a callback parameter on %s",async origin=>{
 vi.stubGlobal("location",new URL(origin)); render(<OAuthForm {...props()} />); await fill(); state.fetch.mockResolvedValueOnce(reply({id:"credential"})).mockResolvedValueOnce(reply({auth_url:"https://provider.invalid/authorize"})); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); fireEvent.change(screen.getByLabelText("Manual authorization code or redirect URL"),{target:{value:"code"}}); state.fetch.mockResolvedValueOnce(reply({})); fireEvent.click(screen.getByRole("button",{name:"Submit"})); await flush(); const call=state.fetch.mock.calls.find(([url])=>String(url).includes("/exchange"))!; expect(JSON.parse(call[1].body).redirect_uri).toBe("")
})
it("publishes a current action and refuses incomplete or duplicate authorization",async()=>{
 const changed=vi.fn(); render(<OAuthForm {...props()} onActionChange={changed} />); await flush(); expect(changed.mock.calls.at(-1)![0].disabled).toBe(true); await act(async()=>{changed.mock.calls.at(-1)![0].authorize()}); expect(state.error).toHaveBeenCalledWith("Client ID, Client Secret, Auth URL, and Token URL are required")
 await fill(); const action=changed.mock.calls.at(-1)![0]; expect(action.disabled).toBe(false); expect(screen.queryByRole("button",{name:"Authorize"})).not.toBeInTheDocument(); act(()=>{action.authorize();action.authorize()}); await flush(); expect(state.fetch.mock.calls.filter(([,init])=>init?.method==="POST")).toHaveLength(2); expect(changed.mock.calls.at(-1)![0]).toMatchObject({disabled:true,busy:true,label:"Waiting for authorization..."})
})
it("shows surface scope labels while retaining full requested scopes",async()=>{
 state.fetch.mockResolvedValueOnce(reply({google:{...provider,default_scopes:"https://provider.invalid/auth/drive/ email,openid"},github:{...provider,default_scopes:""}})); render(<OAuthForm {...props()} variant="surface" />); await flush(); expect(screen.getByText("drive · email · openid")).toBeInTheDocument(); expect(screen.getAllByText("Not configured on this server").length).toBeGreaterThan(0); fireEvent.click(screen.getByRole("button",{name:/Google/})); expect(screen.getByLabelText("Scopes")).toHaveValue("https://provider.invalid/auth/drive/ email,openid"); fireEvent.click(screen.getByRole("button",{name:/Custom/})); expect(screen.getByLabelText("Scopes")).toHaveValue("")
})
it("encodes workspace and credential IDs in every request",async()=>{
 const p={...props(),workspaceId:"workspace&scope=other"}; render(<OAuthForm {...p} />); await fill(); state.fetch.mockResolvedValueOnce(reply({id:"credential/slash"})); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); await act(async()=>vi.advanceTimersByTimeAsync(2000)); const urls=state.fetch.mock.calls.map(([url])=>new URL(url,"http://localhost")); expect(urls.every(url=>url.searchParams.getAll("workspace_id").join()===p.workspaceId)).toBe(true); expect(urls.some(url=>url.pathname.endsWith("/credential%2Fslash"))).toBe(true)
})
it.each(["http://localhost","https://public.example","http://10.0.0.5"])("abandons a pending authorization-start request on %s",async origin=>{
 vi.stubGlobal("location",new URL(origin)); const p=props(); const view=render(<OAuthForm {...p} />); await fill(); const pending=deferred(); state.fetch.mockResolvedValueOnce(reply({id:"credential"})).mockReturnValueOnce(pending.promise); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); view.unmount(); await act(async()=>pending.resolve(reply({auth_url:"https://provider.invalid/auth"}))); await flush(); expect(window.open).not.toHaveBeenCalled(); expect(state.error).not.toHaveBeenCalled(); expect(p.onSelectCredential).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0)
})
for(const status of [200,400])it.each(["http://localhost","https://public.example","http://10.0.0.5"])(`abandons a pending ${status} authorization body on %s`,async origin=>{
 vi.stubGlobal("location",new URL(origin)); const view=render(<OAuthForm {...props()} />); await fill(); const pending=deferred(); const res=reply({},status); vi.spyOn(res,"json").mockImplementation(()=>pending.promise.then(result=>result.json())); state.fetch.mockResolvedValueOnce(reply({id:"credential"})).mockResolvedValueOnce(res); fireEvent.click(screen.getByRole("button",{name:"Authorize"})); await flush(); view.unmount(); await act(async()=>pending.resolve(reply({auth_url:"https://provider.invalid/auth",error:"Obsolete error"}))); await flush(); expect(window.open).not.toHaveBeenCalled(); expect(state.error).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0)
})
it("discards provider discovery after unmount",async()=>{
 const pending=deferred(); state.fetch.mockReturnValueOnce(pending.promise); const view=render(<OAuthForm {...props()} />); view.unmount(); await act(async()=>pending.resolve(reply({google:provider}))); await flush(); expect(state.error).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0)
})
it("submits a manual code only once while the exchange is pending",async()=>{
 const p=props(); render(<OAuthForm {...p} />); await authorize(); fireEvent.change(screen.getByLabelText("Manual authorization code or redirect URL"),{target:{value:"code"}}); const pending=deferred(); state.fetch.mockReturnValueOnce(pending.promise); fireEvent.click(screen.getByRole("button",{name:"Submit"})); fireEvent.click(screen.getByRole("button",{name:"Submit"})); expect(state.fetch.mock.calls.filter(([url])=>String(url).includes("/exchange"))).toHaveLength(1); await act(async()=>pending.resolve(reply({}))); expect(p.onSelectCredential).toHaveBeenCalledOnce()
})
