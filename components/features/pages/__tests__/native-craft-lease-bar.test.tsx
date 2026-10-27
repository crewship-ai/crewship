import {render,screen,fireEvent} from "@testing-library/react"
import {describe,it,expect,vi} from "vitest"
import {NativeCraftLeaseBar,type NativeCraftLeaseView} from "../native-craft-lease-bar"
const view:NativeCraftLeaseView={holder:{actor_id:"user:private-id",display_name:"Pavel",kind:"human"},mine:false,generation:"9007199254740995",can_write:false,acquired_at:null,handoff_requested_by:[]}
describe("native Craft lease controls",()=>{
 it("shows another holder and requests handoff without granting a write",()=>{
  const onAction=vi.fn();render(<NativeCraftLeaseBar view={view} mayEdit onAction={onAction}/>)
  expect(screen.getByText(/Upravuje Pavel/)).toBeInTheDocument()
  expect(screen.queryByRole("button",{name:/Uvolnit úpravy|Převzít úpravy/i})).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole("button",{name:"Požádat o předání"}))
  expect(onAction).toHaveBeenCalledWith("request-handoff",{})
  expect(screen.queryByText(/private-id/)).not.toBeInTheDocument()
 })
 it("passes the exact generation and public request ID for handoff and release",()=>{
  const onAction=vi.fn();render(<NativeCraftLeaseBar view={{...view,mine:true,can_write:true,handoff_requested_by:[{request_id:"public-request",display_name:"Alice",kind:"human"}]}} mayEdit onAction={onAction}/>)
  fireEvent.click(screen.getByRole("button",{name:"Předat uživateli Alice"}))
  expect(onAction).toHaveBeenCalledWith("handoff",{generation:"9007199254740995",request_id:"public-request"})
  fireEvent.click(screen.getByRole("button",{name:"Uvolnit úpravy"}))
  expect(onAction).toHaveBeenLastCalledWith("release",{generation:"9007199254740995"})
 })
 it("keeps a read-only viewer from invoking editing actions",()=>{
  render(<NativeCraftLeaseBar view={{...view,holder:null,generation:null}} mayEdit={false} onAction={vi.fn()}/>)
  expect(screen.getByText("Pouze čtení")).toBeInTheDocument()
  expect(screen.queryByRole("button")).not.toBeInTheDocument()
 })
})
