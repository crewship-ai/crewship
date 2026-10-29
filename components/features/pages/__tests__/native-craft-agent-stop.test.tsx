import {render,screen,fireEvent} from "@testing-library/react"
import {describe,it,expect,vi} from "vitest"
import {NativeCraftLeaseBar,type NativeCraftLeaseView} from "../native-craft-lease-bar"
const view:NativeCraftLeaseView={holder:{actor_id:"agent:private-id",display_name:"Jamie",kind:"agent"},mine:false,generation:"7",can_write:false,acquired_at:null,handoff_requested_by:[],can_stop_agent:true}
describe("native Craft agent stop",()=>{
 it("shows the actual agent and lets the authorized human stop its mandate",()=>{
  const stop=vi.fn(),onAction=vi.fn()
  render(<NativeCraftLeaseBar view={view} mayEdit onAction={onAction} onStopAgent={stop}/>)
  expect(screen.getByText(/Upravuje Jamie/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button",{name:"Zastavit agenta"}))
  expect(stop).toHaveBeenCalledOnce();expect(onAction).not.toHaveBeenCalled()
  expect(screen.queryByText(/private-id/)).not.toBeInTheDocument()
 })
 it.each([{...view,can_stop_agent:false},{...view,holder:{actor_id:"user:human",display_name:"Pavel",kind:"human" as const}}])("does not offer stop for another grant or human holder",state=>{
  render(<NativeCraftLeaseBar view={state} mayEdit onAction={vi.fn()} onStopAgent={vi.fn()}/>)
  expect(screen.queryByRole("button",{name:"Zastavit agenta"})).not.toBeInTheDocument()
 })
 it("keeps a pending stop from starting a second mutation",()=>{
  render(<NativeCraftLeaseBar view={view} mayEdit pending onAction={vi.fn()} onStopAgent={vi.fn()}/>)
  expect(screen.getByRole("button",{name:"Zastavit agenta"})).toBeDisabled()
 })
})
