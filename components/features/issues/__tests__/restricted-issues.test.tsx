import {afterEach,expect,it,vi} from "vitest"
import {cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react"
import {QueryClient,QueryClientProvider} from "@tanstack/react-query"
import {RestrictedIssues} from "../restricted-issues"

const fetchMock=vi.hoisted(()=>vi.fn())
vi.mock("@/lib/api-fetch",()=>({apiFetch:fetchMock}))
vi.mock("@/hooks/use-workspace",()=>({useWorkspace:()=>({workspaceId:"w",workspace:{name:"Workspace"}})}))
vi.mock("@/hooks/use-auth",()=>({useSession:()=>({data:{user:{id:"guest"}}}),useSessionSafe:()=>({data:null})}))
vi.mock("@/hooks/use-access-mode",()=>({useRestrictedSurfaces:()=>["issues","account_security"]}))
afterEach(()=>{cleanup();fetchMock.mockReset()})
const row={id:"issue-1",identifier:"CLIENT-1",title:"Shared issue",status:"TODO",project_id:"project-1",project_name:"Client project",can_read:true}
function show(identifier?:string){
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 render(<QueryClientProvider client={client}><RestrictedIssues identifier={identifier}/></QueryClientProvider>)
}
it("uses the project-filtered projection and clears rendered data on a rejected refresh",async()=>{
 fetchMock.mockResolvedValueOnce(new Response(JSON.stringify([row]))).mockResolvedValueOnce(new Response("{}",{status:404}))
 show()
 await screen.findByRole("link",{name:"Shared issue"})
 expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/w/restricted-issues?limit=100&offset=0")
 fireEvent.click(screen.getByRole("button",{name:"Refresh"}))
 await screen.findByText("Issues unavailable")
 expect(screen.queryByText(/this is not about your access/)).toBeNull()
 expect(screen.getByText(/Your workspace administrator decides/)).toBeInTheDocument()
 expect(screen.queryByRole("link",{name:"Shared issue"})).toBeNull()
 expect(fetchMock.mock.calls).toHaveLength(2)
})
it("does not offer details without project read access",async()=>{
 fetchMock.mockResolvedValue(new Response(JSON.stringify([{...row,can_read:false}])))
 show()
 await screen.findByRole("heading",{name:"Shared issue"})
 expect(screen.queryByRole("link",{name:"Shared issue"})).toBeNull()
})
it("reads only the requested issue detail and renders no mutation form",async()=>{
 fetchMock.mockResolvedValue(new Response(JSON.stringify({...row,description:"Current project brief"})))
 show("CLIENT-1")
 await screen.findByText("Current project brief")
 await waitFor(()=>expect(fetchMock).toHaveBeenCalledTimes(1))
 expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/w/restricted-issues/CLIENT-1")
 expect(screen.queryByRole("textbox")).toBeNull()
 expect(screen.queryByRole("button",{name:/save|delete|start/i})).toBeNull()
})
