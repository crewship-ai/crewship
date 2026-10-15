import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { SharedProjectMilestones } from "../shared-project-milestones"
const fetchMock=vi.hoisted(()=>vi.fn())
vi.mock("@/lib/api-fetch",()=>({apiFetch:fetchMock}))
const item={id:"m1",project_id:"p1",name:"Release",description:"Client brief",target_date:"2026-11-01",status:"active",position:1,version:"a".repeat(64),can_manage:true,can_delete:true}
function response(body:unknown,status=200,canManage=true,total=1){return {ok:status<400,status,headers:new Headers({"X-Total-Count":String(total),"X-Can-Manage":String(canManage)}),json:async()=>body}}
function show(){const client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});render(<QueryClientProvider client={client}><SharedProjectMilestones workspaceId="w1" projectId="p1" userId="guest-a" /></QueryClientProvider>);return client}
afterEach(()=>{cleanup();vi.clearAllMocks()})
describe("shared project milestones",()=>{
 it("uses the current credential's permission for create and row actions",async()=>{
  fetchMock.mockResolvedValue(response([{...item,can_manage:false,can_delete:false}],200,false));show()
  await screen.findByRole("article",{name:"Milestone Release"})
  expect(screen.queryByText("New milestone",{exact:true})).toBeNull()
  expect(screen.queryByText("Edit milestone",{exact:true})).toBeNull()
  expect(screen.queryByRole("button",{name:"Remove milestone"})).toBeNull()
 })
 it("creates only the allowed metadata and preserves a failed draft",async()=>{
  fetchMock.mockImplementation(async(_path:string,init?:RequestInit)=>init?.method==="POST"?response({},500):response([],200,true,0));show()
  fireEvent.click(await screen.findByText("New milestone",{exact:true}))
  fireEvent.change(screen.getByLabelText("Milestone name",{exact:true}),{target:{value:"Client release"}})
  fireEvent.change(screen.getByLabelText("Milestone description",{exact:true}),{target:{value:"Preserved roadmap"}})
  fireEvent.click(screen.getByRole("button",{name:"Create milestone"}))
  await screen.findByText(/Milestone was not saved/)
  expect(screen.getByLabelText("Milestone name",{exact:true})).toHaveValue("Client release")
  const writes=fetchMock.mock.calls.filter(([,init])=>init?.method==="POST")
  expect(writes).toHaveLength(1)
  expect(JSON.parse(String(writes[0][1].body))).toEqual({name:"Client release",description:"Preserved roadmap",target_date:"",status:"active"})
 })
 it("pins an edit version and preserves the draft across a conflicting refresh",async()=>{
  let current=item
  fetchMock.mockImplementation(async(_path:string,init?:RequestInit)=>{
   if(init?.method==="PUT"){current={...item,name:"Staff release",version:"b".repeat(64)};return response({},409)}
   return response([current])
  });show()
  const card=await screen.findByRole("article",{name:"Milestone Release"})
  fireEvent.click(within(card).getByText("Edit milestone",{exact:true}))
  const name=within(card).getByLabelText("Milestone name",{exact:true})
  fireEvent.change(name,{target:{value:"Client draft"}})
  fireEvent.click(within(card).getByRole("button",{name:"Save milestone"}))
  await screen.findByText(/Your draft is kept; review/)
  expect(name).toHaveValue("Client draft")
  expect(within(card).getByRole("button",{name:"Save milestone"})).toBeDisabled()
  const write=fetchMock.mock.calls.find(([,init])=>init?.method==="PUT")!
  expect(JSON.parse(String(write[1].body)).version).toBe(item.version)
  fireEvent.click(within(card).getByRole("button",{name:"Load current milestone"}))
  expect(name).toHaveValue("Staff release")
 })
 it("keeps the reviewed deletion snapshot when a background update arrives",async()=>{
  let current=item;fetchMock.mockImplementation(async()=>response([current]))
  const client=show();const card=await screen.findByRole("article",{name:"Milestone Release"})
  fireEvent.click(within(card).getByRole("button",{name:"Remove milestone"}))
  await screen.findByRole("alertdialog",{name:"Remove Release?"})
  current={...item,name:"New release",version:"b".repeat(64)}
  await act(async()=>{await client.invalidateQueries({queryKey:["restricted-project-milestones"]})})
  expect(screen.getByRole("alertdialog",{name:"Remove Release?"})).toBeInTheDocument()
  await waitFor(()=>expect(screen.getByRole("button",{name:"Remove empty milestone"})).toBeDisabled())
  expect(fetchMock.mock.calls.some(([,init])=>init?.method==="DELETE")).toBe(false)
 })
 it("clears milestone content when access is refused",async()=>{
  let lost=false;fetchMock.mockImplementation(async()=>lost?response({},404):response([item]))
  const client=show();await screen.findByRole("article",{name:"Milestone Release"})
  lost=true;await act(async()=>{await client.invalidateQueries({queryKey:["restricted-project-milestones"]})})
  await waitFor(()=>expect(screen.queryByRole("article")).toBeNull())
  expect(screen.queryByText("New milestone",{exact:true})).toBeNull()
 })
})
