import React, {StrictMode,useState} from "react"
import {beforeEach,afterEach,it,expect,vi} from "vitest"
import {act,cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react"
import {DialogTitle} from "@/components/ui/dialog"
import {CreateSurface} from "../create-surface"
import {WorkspaceAvailabilityBoundary} from "../workspace-availability-boundary"
import {useWorkspace,refreshWorkspaceSettings,_resetWorkspaceStoreForTests} from "@/hooks/use-workspace"
import {useUnsavedNavigationGuard} from "@/hooks/use-unsaved-navigation-guard"
import {apiFetch} from "@/lib/api-fetch"
import {WorkspaceUnavailableError} from "@/lib/workspace-availability"
vi.mock("next/navigation",()=>({useRouter:()=>({push:vi.fn()})}))
const A={id:"a",name:"Acme",slug:"a",currentUserRole:"OWNER"}
const B={id:"b",name:"Beta",slug:"b",currentUserRole:"OWNER"}
let rows=[A,B]
const network=vi.fn()
function Draft(){
 const [value,setValue]=useState("")
 const {workspaceId}=useWorkspace()
 useUnsavedNavigationGuard(value!=="","Discard draft?")
 return <CreateSurface open onOpenChange={()=>{}} dirty={value!==""} ariaLabel="Draft editor"><span data-testid="draft-workspace">{workspaceId}</span><DialogTitle>Draft editor</DialogTitle>
  <label>Local draft<input aria-label="Local draft" value={value} onChange={e=>setValue(e.target.value)}/></label>
  <button onClick={()=>{void apiFetch(`/api/v1/pages?workspace_id=${workspaceId}`,{method:"POST"}).catch(()=>{})}}>Save draft</button>
 </CreateSurface>
}
beforeEach(()=>{
 cleanup();_resetWorkspaceStoreForTests();rows=[A,B];network.mockReset()
 network.mockImplementation(async(input:RequestInfo|URL)=>new Response(JSON.stringify(String(input)==="/api/v1/workspaces"?rows:{}),{status:200,headers:{"Content-Type":"application/json"}}))
 vi.stubGlobal("fetch",network)
 vi.stubGlobal("confirm",vi.fn().mockReturnValue(false))
})
afterEach(()=>{cleanup();vi.restoreAllMocks();vi.unstubAllGlobals();_resetWorkspaceStoreForTests()})
async function mountDraft(){
 render(<StrictMode><WorkspaceAvailabilityBoundary><Draft/></WorkspaceAvailabilityBoundary></StrictMode>)
 await waitFor(()=>expect(network).toHaveBeenCalled())
 await waitFor(()=>expect(screen.getByTestId("draft-workspace")).toHaveTextContent("a"))
 const dialog=screen.getByRole("dialog",{name:"Draft editor"})
 const attributes={aria:dialog.getAttribute("aria-hidden"),inert:dialog.getAttribute("inert")}
 fireEvent.change(screen.getByLabelText("Local draft"),{target:{value:"typed local draft"}})
 rows=[B]
 await act(async()=>{await refreshWorkspaceSettings()})
 await screen.findByRole("dialog",{name:"Selected workspace is unavailable"})
 return {dialog,attributes}
}
it("occludes portals and blocks writes, then restores the same draft on recovery",async()=>{
 const {dialog,attributes}=await mountDraft()
 const latePortal=document.createElement("div")
 latePortal.setAttribute("role","alertdialog")
 latePortal.setAttribute("aria-hidden","false")
 document.body.append(latePortal)
 await waitFor(()=>expect(latePortal.getAttribute("inert")).toBe(""))
 expect(screen.queryByRole("dialog",{name:"Draft editor"})).toBeNull()
 const priorCalls=network.mock.calls.length
 fireEvent.click(screen.getByText("Save draft"))
 await expect(apiFetch("/api/v1/pages?workspace_id=a",{method:"POST"})).rejects.toBeInstanceOf(WorkspaceUnavailableError)
 expect(network).toHaveBeenCalledTimes(priorCalls)
 fireEvent.keyDown(screen.getByRole("dialog",{name:"Selected workspace is unavailable"}),{key:"Escape"})
 expect(screen.getByRole("dialog",{name:"Selected workspace is unavailable"})).toBeTruthy()
 rows=[A,B]
 fireEvent.click(screen.getByRole("button",{name:"Retry workspace"}))
 await waitFor(()=>expect(screen.queryByRole("dialog",{name:"Selected workspace is unavailable"})).toBeNull())
 expect(screen.getByLabelText("Local draft")).toHaveValue("typed local draft")
 expect(dialog.getAttribute("aria-hidden")).toBe(attributes.aria)
 expect(dialog.getAttribute("inert")).toBe(attributes.inert)
 expect(latePortal.getAttribute("aria-hidden")).toBe("false")
 expect(latePortal.getAttribute("inert")).toBeNull()
 latePortal.remove()
})
it("requires the existing draft guard before an explicit context change",async()=>{
 await mountDraft()
 fireEvent.click(screen.getByRole("button",{name:"Choose Beta"}))
 expect(window.confirm).toHaveBeenCalled()
 expect(screen.getByRole("dialog",{name:"Selected workspace is unavailable"})).toBeTruthy()
 vi.mocked(window.confirm).mockReturnValue(true)
 fireEvent.click(screen.getByRole("button",{name:"Choose Beta"}))
 await waitFor(()=>expect(screen.queryByRole("dialog",{name:"Selected workspace is unavailable"})).toBeNull())
 expect(screen.getByLabelText("Local draft")).toHaveValue("")
})
