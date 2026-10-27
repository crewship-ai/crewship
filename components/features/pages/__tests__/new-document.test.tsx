import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { NewDocumentDialog } from "../new-document-dialog"
const mocks=vi.hoisted(()=>({fetch:vi.fn(),allowed:true,enabled:true,commands:false,native:false,sdkPrefix:undefined as string|undefined,checkpoint:vi.fn(),openNative:vi.fn(),openCommand:vi.fn()}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:mocks.fetch,broadcastSessionExpired:vi.fn()}))
vi.mock("@/hooks/use-page-creation-access",()=>({usePageCreationAccess:()=>mocks.allowed}))
vi.mock("@/hooks/use-documents-availability",()=>({useDocumentsAvailability:()=>({data:mocks.enabled,wordcraftSDKPrefix:mocks.sdkPrefix,wordcraftCommands:mocks.commands,wordcraftNative:mocks.native})}))
vi.mock("@/lib/mooring/wordcraft-create",()=>({createEmptyWordCraftCheckpoint:mocks.checkpoint}))
vi.mock("@/lib/mooring/wordcraft-host-session",()=>({openWordCraftSession:mocks.openNative}))
vi.mock("@/lib/mooring/wordcraft-command-session",()=>({commandRequest:mocks.openCommand}))
afterEach(()=>{cleanup();mocks.fetch.mockReset();mocks.allowed=true;mocks.enabled=true;mocks.commands=false;mocks.native=false;mocks.sdkPrefix=undefined;mocks.checkpoint.mockReset();mocks.openNative.mockReset();mocks.openCommand.mockReset()})
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

it("offers commands only when explicitly available and creates only the new empty Page",async()=>{
  mocks.commands=true;mocks.enabled=false
  mocks.fetch.mockImplementation(()=>response({id:"command-page",slug:"notes"}))
  mocks.openCommand.mockResolvedValue({outcome:null,projection:{revision:0}})
  const {created,client}=mount()
  expect(screen.getByLabelText("Format")).toHaveValue("commands")
  expect(screen.getByRole("option",{name:"WordCraft (test příkazů)"})).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button",{name:"Create Doc"}))
  await waitFor(()=>expect(created).toHaveBeenCalledWith("notes"))
  expect(JSON.parse(mocks.fetch.mock.calls[0][1].body)).toMatchObject({panels:[],allow_empty:true})
  expect(mocks.openCommand).toHaveBeenCalledWith("workspace","command-page",{action:"open",write:true,create:true},expect.any(AbortSignal))
  expect(mocks.openNative).not.toHaveBeenCalled();expect(mocks.checkpoint).not.toHaveBeenCalled();client.clear()
})
it("checks the same command Page after uncertain creation without repeating initialize or falling back",async()=>{
  mocks.commands=true;mocks.enabled=false
  mocks.fetch.mockImplementation(()=>response({id:"command-page",slug:"notes"}))
  mocks.openCommand.mockRejectedValueOnce(new Error("unknown initialization")).mockResolvedValueOnce({outcome:null,projection:{revision:0}})
  const {created,client}=mount()
  fireEvent.click(screen.getByRole("button",{name:"Create Doc"}));await screen.findByRole("alert")
  fireEvent.click(screen.getByRole("button",{name:"Retry initialization"}))
  await waitFor(()=>expect(created).toHaveBeenCalledWith("notes"))
  expect(mocks.fetch).toHaveBeenCalledOnce()
  expect(mocks.openCommand.mock.calls.map(call=>[call[1],call[2]])).toEqual([["command-page",{action:"open",write:true,create:true}],["command-page",{action:"open",write:true}]])
  expect(mocks.openNative).not.toHaveBeenCalled();client.clear()
})
it("does not offer command creation while its availability is off",()=>{
  const {client}=mount()
  expect(screen.queryByRole("option",{name:"WordCraft (test příkazů)"})).toBeNull()
  expect(mocks.openCommand).not.toHaveBeenCalled();client.clear()
})

it("creates whole-native Word without invoking checkpoint or command engines",async()=>{
  mocks.native=true;mocks.enabled=false
  mocks.fetch.mockImplementation((url:string)=>url.includes("/craft/initialize")?response({application_id:"wordcraft",page_id:"native-page",session_token:"a".repeat(64),generation:"1",version:"1"},201):url.includes("/lease/release")?response({holder:null}):response({id:"native-page",slug:"notes"},201))
  const {created,client}=mount();fireEvent.click(screen.getByRole("button",{name:"Create Doc"}))
  await waitFor(()=>expect(created).toHaveBeenCalledWith("notes","native-page"))
  expect(mocks.openNative).not.toHaveBeenCalled();expect(mocks.openCommand).not.toHaveBeenCalled()
  const call=mocks.fetch.mock.calls.find(([url])=>url.includes("/craft/initialize"))!
  expect(JSON.parse(call[1].body).application_id).toBe("wordcraft")
  expect(JSON.parse(atob(JSON.parse(call[1].body).bytes)).body).toBeDefined()
  expect(mocks.fetch.mock.calls.find(([url])=>url.includes("/lease/release"))![1].headers["X-Craft-Session"]).toBe("a".repeat(64))
  client.clear()
})
it("never retries an ambiguous native initialization against the original Page",async()=>{
  mocks.native=true;mocks.enabled=false
  mocks.fetch.mockImplementation((url:string)=>url.includes("/craft/initialize")?response({},503):response({id:"native-page",slug:"notes"},201))
  const {created,client}=mount();fireEvent.click(screen.getByRole("button",{name:"Create Doc"}));await screen.findByRole("alert")
  fireEvent.click(screen.getByRole("button",{name:"Retry initialization"}));await waitFor(()=>expect(screen.getByRole("alert")).toHaveTextContent("could not be confirmed"))
  expect(mocks.fetch.mock.calls.filter(([url])=>url.includes("/craft/initialize"))).toHaveLength(1)
  expect(mocks.fetch.mock.calls.filter(([url])=>!url.includes("/craft/"))).toHaveLength(1)
  expect(created).not.toHaveBeenCalled();client.clear()
})

it.each(["lost release acknowledgment","expired creation lease"])("opens the confirmed native document after %s without initializing again",async(mode)=>{
 mocks.native=true;mocks.enabled=false
 mocks.fetch.mockImplementation((url:string)=>url.includes("/craft/initialize")?response({application_id:"wordcraft",page_id:"native-page",session_token:"a".repeat(64),generation:"1",version:"1"},201):url.includes("/lease/release")?mode.startsWith("lost")?Promise.reject(new TypeError("connection lost")):response({},409):url.includes("/craft/availability")?response({enabled:true}):response({id:"native-page",slug:"notes"},201))
 const {created,client}=mount();fireEvent.click(screen.getByRole("button",{name:"Create Doc"}))
 await waitFor(()=>expect(created).toHaveBeenCalledWith("notes","native-page"))
 expect(mocks.fetch.mock.calls.filter(([url])=>url.includes("/craft/initialize"))).toHaveLength(1)
 client.clear()
})
