import {beforeEach,describe,expect,it,vi} from "vitest"
import {renderHook} from "@testing-library/react"
const state=vi.hoisted(()=>({workspaceId:"ws-1",role:"VIEWER" as string|null,capabilities:[] as string[],loading:false,error:false,workspaces:[{id:"ws-1"}] as {id:string;currentUserAccessMode?:"trusted"|"restricted"}[]}))
vi.mock("@/hooks/use-workspace", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/use-workspace")>(),
  useWorkspace: () => state,
}))
import {usePageCreationAccess} from "@/hooks/use-page-creation-access"
describe("Page creation affordance",()=>{
 beforeEach(()=>Object.assign(state,{workspaceId:"ws-1",role:"VIEWER",capabilities:[],loading:false,error:false,workspaces:[{id:"ws-1"}]}))
 it.each(["OWNER","ADMIN","MANAGER"])("allows %s",role=>{state.role=role;expect(renderHook(()=>usePageCreationAccess("ws-1")).result.current).toBe(true)})
 it.each(["MEMBER","VIEWER"])("requires a capability for %s",role=>{state.role=role;const h=renderHook(()=>usePageCreationAccess("ws-1"));expect(h.result.current).toBe(false);state.capabilities=["page.create"];h.rerender();expect(h.result.current).toBe(true)})
 it("fails closed for loading, failure, wrong workspace and account-wide restricted membership",()=>{
  state.role="OWNER"
  const h=renderHook(()=>usePageCreationAccess("ws-1"));expect(h.result.current).toBe(true)
  state.loading=true;h.rerender();expect(h.result.current).toBe(false)
  state.loading=false;state.error=true;h.rerender();expect(h.result.current).toBe(false)
  state.error=false;state.workspaceId="ws-2";h.rerender();expect(h.result.current).toBe(false)
  state.workspaceId="ws-1";state.workspaces=[{id:"ws-1"},{id:"ws-2",currentUserAccessMode:"restricted"}];h.rerender();expect(h.result.current).toBe(false)
 })
})
