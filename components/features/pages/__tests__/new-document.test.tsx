import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import { NewArtifactDialog } from "../new-artifact-dialog"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-pages", () => ({ pagesKeys: { all: (workspace: string) => ["pages", workspace] } }))
afterEach(() => { cleanup(); request.mockReset() })
it("creates a human-owned empty page before initializing the durable document", async () => {
  request.mockResolvedValue({ ok: true, json: async () => ({ enabled: true }) })
  const created = vi.fn()
  render(<QueryClientProvider client={new QueryClient()}><NewArtifactDialog workspaceId="w" onClose={vi.fn()} onCreated={created} /></QueryClientProvider>)
  await waitFor(() => expect(screen.getByRole("option", { name: "Doc" })).toBeEnabled())
  fireEvent.change(screen.getByLabelText("Content"), { target: { value: "document" } })
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Team notes" } })
  fireEvent.click(screen.getByRole("button", { name: "Create Doc page" }))
  await waitFor(() => expect(created).toHaveBeenCalledWith("team-notes"))
  expect(request.mock.calls.map(([url]) => url)).toEqual(["/api/v1/documents/status?workspace_id=w", "/api/v1/pages?workspace_id=w", "/api/v1/pages/team-notes/document/initialize?workspace_id=w"])
  expect(JSON.parse(request.mock.calls[1][1].body)).not.toHaveProperty("owner")
  expect(JSON.parse(request.mock.calls[2][1].body)).toMatchObject({ schema: 2, body: { type: "doc", content: [{ type: "paragraph", attrs: { id: expect.any(String) } }] } })
})
for (const kind of ["document", "slides"]) it(`retries uncertain ${kind} initialization with exact original payload and frozen form`, async () => {
  const created=vi.fn();let first:string|undefined,initializations=0
  request.mockImplementation(async (url:string,options?:{body?:string})=>{
    if(url.includes("/documents/status"))return{ok:true,json:async()=>({enabled:true})}
    if(url==="/api/v1/pages?workspace_id=w")return{ok:true,json:async()=>({id:"original_page"})}
    if(url.endsWith(`/${kind === "document" ? "document" : "slides"}/initialize?workspace_id=w`)){
      initializations++
      if(!first){first=options?.body;throw new Error("acknowledgement lost")}
      expect(options?.body).toBe(first)
      if(kind==="slides")expect(JSON.parse(first!)).toMatchObject({page_id:"original_page",deck:{deck_id:"original_page"}})
      return{ok:true,json:async()=>({})}
    }
    throw new Error(`Unexpected request ${url}`)
  })
  render(<QueryClientProvider client={new QueryClient()}><NewArtifactDialog workspaceId="w" onClose={vi.fn()} onCreated={created}/></QueryClientProvider>)
  await waitFor(()=>expect(screen.getByRole("option",{name:"Doc"})).toBeEnabled())
  fireEvent.change(screen.getByLabelText("Content"),{target:{value:kind}})
  fireEvent.change(screen.getByLabelText("Name"),{target:{value:"Original intent"}})
  const label=kind==="document"?"Create Doc page":"Create Slides page"
  fireEvent.click(screen.getByRole("button",{name:label}))
  await screen.findByRole("alert")
  for(const label of ["Content","Name","Page address"])expect(screen.getByLabelText(label)).toBeDisabled()
  fireEvent.click(screen.getByRole("button",{name:label}))
  await waitFor(()=>expect(created).toHaveBeenCalledWith("original-intent"))
  expect(initializations).toBe(2);expect(request.mock.calls.filter(([url])=>url==="/api/v1/pages?workspace_id=w")).toHaveLength(1)
})
it("freezes the initial intent while Page creation is pending and never adopts an uncertain slug", async()=>{
  let finish!:(value:unknown)=>void,creates=0
  request.mockImplementation(async(url:string)=>{
    if(url.includes("/documents/status"))return{ok:true,json:async()=>({enabled:true})}
    if(url==="/api/v1/pages?workspace_id=w"){
      creates++;if(creates===1)return new Promise(resolve=>{finish=resolve})
      return{ok:false,status:409,json:async()=>({error:"Page already exists"})}
    }
    throw new Error("No initialization permitted without original Page identity")
  })
  render(<QueryClientProvider client={new QueryClient()}><NewArtifactDialog workspaceId="w" onClose={vi.fn()} onCreated={vi.fn()}/></QueryClientProvider>)
  fireEvent.change(screen.getByLabelText("Content"),{target:{value:"slides"}});fireEvent.change(screen.getByLabelText("Name"),{target:{value:"Frozen"}})
  fireEvent.click(screen.getByRole("button",{name:"Create Slides page"}))
  await waitFor(()=>expect(screen.getByLabelText("Content")).toBeDisabled());expect(screen.getByLabelText("Name")).toBeDisabled();expect(screen.getByLabelText("Page address")).toBeDisabled()
  finish(Promise.reject(new Error("creation acknowledgement lost")))
  await screen.findByRole("alert");fireEvent.click(screen.getByRole("button",{name:"Create Slides page"}))
  await screen.findByText("Page already exists")
  expect(request.mock.calls.some(([url])=>url.includes("/initialize"))).toBe(false)
  const bodies=request.mock.calls.filter(([url])=>url==="/api/v1/pages?workspace_id=w").map(([,options])=>options.body);expect(bodies[0]).toBe(bodies[1])
})
it("fails closed when a successful Slides Page creation lacks its immutable identity",async()=>{
 request.mockResolvedValue({ok:true,json:async()=>({enabled:true})})
 render(<QueryClientProvider client={new QueryClient()}><NewArtifactDialog workspaceId="w" onClose={vi.fn()} onCreated={vi.fn()}/></QueryClientProvider>)
 fireEvent.change(screen.getByLabelText("Content"),{target:{value:"slides"}});fireEvent.change(screen.getByLabelText("Name"),{target:{value:"Missing identity"}})
 fireEvent.click(screen.getByRole("button",{name:"Create Slides page"}))
 await screen.findByText("Page creation did not return its identity. Reopen the Page before initializing content.")
 expect(request.mock.calls.some(([url])=>url.includes("/initialize"))).toBe(false)
})
