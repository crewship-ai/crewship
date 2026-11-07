import {render,screen,fireEvent} from "@testing-library/react"
import {describe,it,expect,vi} from "vitest"
import {NativeCraftLeaseBar,type NativeCraftLeaseView} from "../native-craft-lease-bar"
const view:NativeCraftLeaseView={holder:{actor_id:"user:private-id",display_name:"Pavel",kind:"human"},mine:false,generation:"9007199254740995",can_write:false,acquired_at:null,handoff_requested_by:[]}
describe("native Craft lease controls",()=>{
 it('updates an existing polite status node when a human handoff request is confirmed',()=>{
  const onAction=vi.fn()
  const {rerender,container}=render(<NativeCraftLeaseBar view={view} mayEdit onAction={onAction}/>)
  const region=container.querySelector('[data-slot="handoff-request-status"]')
  expect(region).not.toBeNull()
  expect(region).toHaveAttribute('aria-live','polite')
  expect(region).toHaveTextContent('')
  rerender(<NativeCraftLeaseBar view={view} mayEdit requested onAction={onAction}/>)
  expect(screen.getByRole('status')).toBe(region)
  expect(region).toHaveTextContent('Čeká se na předání úprav.')
  fireEvent.click(screen.getByRole('button',{name:'Žádost odeslána'}))
  expect(onAction).not.toHaveBeenCalled()
 })
 it.each([false,true])('does not promise an agent handoff, and offers stop only when authorized (%s)',canStop=>{
  render(<NativeCraftLeaseBar view={{...view,holder:{actor_id:'agent:actual',display_name:'Jamie',kind:'agent'},can_stop_agent:canStop}} mayEdit requested onAction={vi.fn()} onStopAgent={vi.fn()}/>)
  expect(screen.getByRole('status')).toHaveTextContent('Požadavek zaznamenán. Agent sám úpravy nepředává.')
  expect(screen.getByRole('status')).not.toHaveTextContent('Čeká se na předání')
  expect(screen.queryByRole('button',{name:'Zastavit agenta'})!==null).toBe(canStop)
 })

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
