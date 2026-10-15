import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { SharedIssueDelete } from "../shared-issue-delete"
const fetchMock=vi.hoisted(()=>vi.fn())
const push=vi.hoisted(()=>vi.fn())
vi.mock("@/lib/api-fetch",()=>({apiFetch:fetchMock}))
vi.mock("next/navigation",()=>({useRouter:()=>({push})}))
const item={id:"i1",identifier:"CLIENT-1",title:"Client request",project_id:"p1",status:"BACKLOG",version:"a".repeat(64),can_delete:true}
const response=(body:unknown,status=200)=>({ok:status<400,status,json:async()=>body})
function show(){const client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});render(<QueryClientProvider client={client}><SharedIssueDelete path="/api/v1/workspaces/w1/restricted-issues/i1" issueId="i1" projectId="p1" userId="guest-a" disabled={false}/></QueryClientProvider>);return client}
afterEach(()=>{cleanup();vi.clearAllMocks()})
describe("explicit issue deletion",()=>{
 it("requires confirmation and sends exactly the reviewed audience/version",async()=>{
  fetchMock.mockImplementation(async(_path:string,init?:RequestInit)=>response(init?.method==="DELETE"?null:item,init?.method==="DELETE"?204:200));show()
  fireEvent.click(await screen.findByRole("button",{name:"Delete issue",exact:true}))
  expect(fetchMock.mock.calls.filter(([,init])=>init?.method==="DELETE")).toHaveLength(0)
  await screen.findByRole("alertdialog",{name:"Delete CLIENT-1?"})
  fireEvent.click(screen.getByRole("button",{name:"Permanently delete issue",exact:true}))
  await waitFor(()=>expect(push).toHaveBeenCalledWith("/issues"))
  const writes=fetchMock.mock.calls.filter(([,init])=>init?.method==="DELETE")
  expect(writes).toHaveLength(1)
  expect(JSON.parse(String(writes[0][1].body))).toEqual({project_id:"p1",version:item.version,confirm:true})
 })
 it("pins confirmation and blocks deletion after a background change",async()=>{
  let current=item;fetchMock.mockImplementation(async()=>response(current));const client=show()
  fireEvent.click(await screen.findByRole("button",{name:"Delete issue",exact:true}));await screen.findByRole("alertdialog")
  current={...item,title:"Updated staff request",version:"b".repeat(64)}
  await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issue-deletion"]})})
  await waitFor(()=>expect(screen.getByRole("button",{name:"Permanently delete issue"})).toBeDisabled())
  expect(screen.getByText("Client request")).toBeInTheDocument()
  expect(screen.queryByText("Updated staff request")).toBeNull()
  fireEvent.click(screen.getByRole("button",{name:"Permanently delete issue"}))
  expect(fetchMock.mock.calls.filter(([,init])=>init?.method==="DELETE")).toHaveLength(0)
 })
 it("hides confirmation on access loss and never exposes stale destructive controls",async()=>{
  let lost=false;fetchMock.mockImplementation(async()=>lost?response({},404):response(item));const client=show()
  fireEvent.click(await screen.findByRole("button",{name:"Delete issue",exact:true}));await screen.findByRole("alertdialog")
  lost=true;await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issue-deletion"]})})
  await waitFor(()=>expect(screen.queryByRole("alertdialog")).toBeNull())
  expect(screen.queryByRole("button",{name:"Delete issue",exact:true})).toBeNull()
 })
 it("requires a fresh confirmation after deletion authority is restored",async()=>{
  let lost=false;fetchMock.mockImplementation(async()=>lost?response({},404):response(item));const client=show()
  fireEvent.click(await screen.findByRole("button",{name:"Delete issue",exact:true}));await screen.findByRole("alertdialog")
  lost=true;await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issue-deletion"]})})
  await waitFor(()=>expect(screen.queryByRole("alertdialog")).toBeNull())
  lost=false;await act(async()=>{await client.invalidateQueries({queryKey:["restricted-issue-deletion"]})})
  await screen.findByRole("button",{name:"Delete issue",exact:true})
  expect(screen.queryByRole("alertdialog")).toBeNull()
 })
 it("rejects a preview from another project",async()=>{
  fetchMock.mockResolvedValue(response({...item,project_id:"p2"}));show()
  await screen.findByText(/issue project changed/)
  expect(screen.queryByRole("button",{name:"Delete issue",exact:true})).toBeNull()
 })
})
