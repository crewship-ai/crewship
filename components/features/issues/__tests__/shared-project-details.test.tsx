import { afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { SharedProjectDetails } from "../shared-project-details"

vi.mock("../shared-project-milestones",()=>({SharedProjectMilestones:()=>null}))

const fetchMock=vi.hoisted(()=>vi.fn())
vi.mock("@/lib/api-fetch",()=>({apiFetch:fetchMock}))
vi.mock("@/hooks/use-auth",()=>({useSession:()=>({data:{user:{id:"guest-a"}}})}))
const initial={id:"p1",name:"Client project",description:"Client brief",version:"a".repeat(64),can_manage:true}
const response=(body:unknown,status=200)=>({ok:status<400,status,json:async()=>body})
function show(){const client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});render(<QueryClientProvider client={client}><SharedProjectDetails workspaceId="w1" projects={[{id:"p1",name:"Client project"}]} /></QueryClientProvider>);fireEvent.click(screen.getByText("Project details"));fireEvent.change(screen.getByLabelText("Shared project"),{target:{value:"p1"}});return client}
afterEach(()=>{cleanup();vi.clearAllMocks()})

describe("shared project details",()=>{
 it("respects read-only current credential hints without offering mutations",async()=>{
  fetchMock.mockResolvedValue(response({...initial,can_manage:false}));show()
  await screen.findByText("Client brief")
  expect(screen.queryByLabelText("Project name")).toBeNull()
  expect(screen.queryByRole("button",{name:"Save project details"})).toBeNull()
 })
 it("uses the reviewed version and keeps a conflict draft until explicit reload",async()=>{
  let current=initial
  fetchMock.mockImplementation(async(_path:string,init?:RequestInit)=>{
   if(init?.method==="PUT"){current={...initial,name:"Staff change",version:"b".repeat(64)};return response({},409)}
   return response(current)
  })
  show();const name=await screen.findByLabelText("Project name")
  fireEvent.change(name,{target:{value:"Client draft"}})
  await waitFor(()=>expect(screen.getByRole("button",{name:"Save project details"})).toBeEnabled())
  fireEvent.click(screen.getByRole("button",{name:"Save project details"}))
  await screen.findByText(/Your draft is kept; load current details/)
  expect(name).toHaveValue("Client draft")
  expect(screen.getByRole("button",{name:"Save project details"})).toBeDisabled()
  const writes=fetchMock.mock.calls.filter(([,init])=>init?.method==="PUT")
  expect(writes).toHaveLength(1)
  expect(JSON.parse(String(writes[0][1].body))).toEqual({name:"Client draft",description:"Client brief",version:initial.version})
  const load=screen.getByRole("button",{name:"Load current details"})
  await waitFor(()=>expect(load).toBeEnabled());fireEvent.click(load)
  expect(name).toHaveValue("Staff change")
 })
 it("keeps edits during a temporary refresh failure and resumes after refresh",async()=>{
  let failed=false
  fetchMock.mockImplementation(async()=>failed?response({},503):response(initial))
  const client=show();const name=await screen.findByLabelText("Project name")
  fireEvent.change(name,{target:{value:"Offline draft"}})
  failed=true;await act(async()=>{await client.invalidateQueries({queryKey:["restricted-project-details"]})})
  await screen.findByRole("alert");expect(name).toHaveValue("Offline draft")
  expect(screen.getByRole("button",{name:"Save project details"})).toBeDisabled()
  failed=false;fireEvent.click(screen.getByRole("button",{name:"Refresh project details"}))
  await waitFor(()=>expect(screen.getByRole("button",{name:"Save project details"})).toBeEnabled())
  expect(name).toHaveValue("Offline draft")
 })
 it("clears the shared details on access loss",async()=>{
  let lost=false;fetchMock.mockImplementation(async()=>lost?response({},404):response(initial))
  const client=show();await screen.findByLabelText("Project name")
  lost=true;await act(async()=>{await client.invalidateQueries({queryKey:["restricted-project-details"]})})
  await waitFor(()=>expect(screen.queryByLabelText("Project name")).toBeNull())
 })
})
