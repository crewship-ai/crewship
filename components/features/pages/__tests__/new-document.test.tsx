import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { NewDocumentDialog } from "../new-document-dialog"
const mocks=vi.hoisted(()=>({fetch:vi.fn(),allowed:true,enabled:true,sdkPrefix:undefined as string|undefined,checkpoint:vi.fn(),openNative:vi.fn()}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:mocks.fetch,broadcastSessionExpired:vi.fn()}))
vi.mock("@/hooks/use-page-creation-access",()=>({usePageCreationAccess:()=>mocks.allowed}))
vi.mock("@/hooks/use-documents-availability",()=>({useDocumentsAvailability:()=>({data:mocks.enabled,wordcraftSDKPrefix:mocks.sdkPrefix})}))
vi.mock("@/lib/mooring/wordcraft-create",()=>({createEmptyWordCraftCheckpoint:mocks.checkpoint}))
vi.mock("@/lib/mooring/wordcraft-host-session",()=>({openWordCraftSession:mocks.openNative}))
afterEach(()=>{cleanup();mocks.fetch.mockReset();mocks.allowed=true;mocks.enabled=true;mocks.sdkPrefix=undefined;mocks.checkpoint.mockReset();mocks.openNative.mockReset()})
function mount(){const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0},mutations:{retry:false}}});const created=vi.fn();render(<QueryClientProvider client={client}><NewDocumentDialog workspaceId="workspace" onClose={vi.fn()} onCreated={created} /></QueryClientProvider>);fireEvent.change(screen.getByLabelText("Name"),{target:{value:"Notes"}});return {created,client}}
const response=(value:unknown,status=200)=>Promise.resolve({ok:status<400,status,json:async()=>value})
it("retries numeric initialization with the original Page and schema3 payload",async()=>{
  let attempts=0
  mocks.fetch.mockImplementation((url:string,init?:RequestInit)=>{
    if(url.includes("/initialize")){attempts++;const body=JSON.parse(init!.body as string);return attempts===1?response({error:"lost acknowledgment"},503):response({id:body.id,rev:0,epoch:1,actor:"human",kind:"init",found:true,duplicate:true})}
    return response({id:"stable-page",slug:"notes"})
  })
  const {created,client}=mount()
  fireEvent.click(screen.getByRole("button",{name:"Create Doc"}))
  await screen.findByRole("alert")
  expect(created).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button",{name:"Retry initialization"}))
  await waitFor(()=>expect(created).toHaveBeenCalledWith("notes"))
  const calls=mocks.fetch.mock.calls
  expect(calls.filter(([url])=>!url.includes("/initialize"))).toHaveLength(1)
  const bodies=calls.filter(([url])=>url.includes("/initialize")).map(([,init])=>JSON.parse(init.body))
  expect(bodies[0]).toEqual(bodies[1]);expect(bodies[0]).toMatchObject({schema:3,page_id:"stable-page"})
  expect(calls[0][1].body).not.toContain("owner")
  client.clear()
})
it("does not initialize or adopt a Page when creation omits its stable identity",async()=>{
  mocks.fetch.mockImplementation(()=>response({slug:"notes"}))
  const {created,client}=mount();fireEvent.click(screen.getByRole("button",{name:"Create Doc"}));await screen.findByRole("alert")
  fireEvent.click(screen.getByRole("button",{name:"Create Doc"}));await waitFor(()=>expect(screen.getByRole("alert")).toHaveTextContent("could not be confirmed"))
  expect(mocks.fetch).toHaveBeenCalledTimes(1);expect(created).not.toHaveBeenCalled();client.clear()
})
it.each(["access","availability"])("blocks creation without confirmed %s",mode=>{
  if(mode==="access")mocks.allowed=false;else mocks.enabled=false
  const {client}=mount();expect(screen.getByRole("button",{name:"Create Doc"})).toBeDisabled();expect(mocks.fetch).not.toHaveBeenCalled();client.clear()
})
it("states the real audience: workspace owners and admins can open a new Doc, so it is not called private",()=>{
  const {client}=mount()
  expect(screen.queryByText(/private/i)).toBeNull()
  expect(screen.getByText("Who can open it")).toBeInTheDocument()
  expect(screen.getByText("You, and workspace owners and admins. Others only after you share it.")).toBeInTheDocument()
  client.clear()
})

it("creates WordCraft from the native service when legacy Docs is disabled",async()=>{
  mocks.enabled=false;mocks.sdkPrefix="/mooring-sdk/generated/"+"a".repeat(64)
  mocks.fetch.mockImplementation(()=>response({id:"native-page",slug:"notes"}))
  mocks.checkpoint.mockResolvedValue("original-checkpoint")
  mocks.openNative.mockResolvedValue({checkpoint_revision:"0"})
  const {created,client}=mount()
  expect(screen.getByLabelText("Format")).toHaveValue("wordcraft")
  fireEvent.click(screen.getByRole("button",{name:"Create Doc"}))
  await waitFor(()=>expect(created).toHaveBeenCalledWith("notes"))
  expect(mocks.fetch).toHaveBeenCalledTimes(1)
  expect(mocks.checkpoint).toHaveBeenCalledWith(mocks.sdkPrefix)
  expect(mocks.openNative).toHaveBeenCalledWith("workspace","native-page",true,expect.any(AbortSignal),"original-checkpoint")
  expect(mocks.fetch.mock.calls[0][1].body).not.toContain("owner")
  client.clear()
})
it("keeps the same native Page and checkpoint after an initialization failure",async()=>{
  mocks.enabled=false;mocks.sdkPrefix="/mooring-sdk/generated/"+"a".repeat(64)
  mocks.fetch.mockImplementation(()=>response({id:"native-page",slug:"notes"}))
  mocks.checkpoint.mockResolvedValue("original-checkpoint")
  mocks.openNative.mockRejectedValueOnce(new Error("WordCraft initialization failed")).mockResolvedValueOnce({checkpoint_revision:"0"})
  const {created,client}=mount()
  fireEvent.click(screen.getByRole("button",{name:"Create Doc"}));await screen.findByRole("alert")
  expect(created).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button",{name:"Retry initialization"}))
  await waitFor(()=>expect(created).toHaveBeenCalledWith("notes"))
  expect(mocks.fetch).toHaveBeenCalledTimes(1);expect(mocks.checkpoint).toHaveBeenCalledTimes(1)
  expect(mocks.openNative.mock.calls.map(call=>[call[1],call[4]])).toEqual([["native-page","original-checkpoint"],["native-page","original-checkpoint"]])
  client.clear()
})
