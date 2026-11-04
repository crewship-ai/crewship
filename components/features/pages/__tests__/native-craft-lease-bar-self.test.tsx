import {render,screen,act} from "@testing-library/react"
import {describe,it,expect,vi,afterEach} from "vitest"
import {NativeCraftLeaseBar,type NativeCraftLeaseView} from "../native-craft-lease-bar"

// UX walkthrough RC-4, R06: after a reload the previous tab's editor session
// still holds the lease until its 15 s TTL lapses. The bar said
// "Upravuje <me> · Požádat o předání" — asking oneself, with no hint that it
// frees by itself.
const now=Date.parse("2026-11-04T10:00:00Z")
const previousTab:NativeCraftLeaseView={holder:{actor_id:"user:me-123",display_name:"PRD QA owner",kind:"human"},mine:false,generation:"7",can_write:false,acquired_at:null,expires_at:new Date(now+12_400).toISOString(),handoff_requested_by:[]}

afterEach(()=>{vi.useRealTimers()})

describe("lease held by the same person in another session",()=>{
 it("says it is the person's other tab and when a closed one frees the lease",()=>{
  vi.useFakeTimers({now})
  render(<NativeCraftLeaseBar view={previousTab} self="me-123" mayEdit onAction={vi.fn()}/>)
  expect(screen.getByText("Upravujete v jiné kartě nebo okně")).toBeInTheDocument()
  expect(screen.queryByText(/Upravuje PRD QA owner/)).not.toBeInTheDocument()
  expect(screen.getByRole("status")).toHaveTextContent("úpravy se uvolní do 13 s")
  act(()=>{vi.advanceTimersByTime(5_000)})
  expect(screen.getByRole("status")).toHaveTextContent("úpravy se uvolní do 8 s")
  act(()=>{vi.advanceTimersByTime(10_000)})
  expect(screen.getByRole("status")).toHaveTextContent("Čekám na uvolnění úprav z jiné karty")
  expect(screen.getByRole("button",{name:"Požádat jinou kartu o předání"})).toBeInTheDocument()
 })

 it("keeps the ordinary holder wording for another person",()=>{
  render(<NativeCraftLeaseBar view={{...previousTab,holder:{actor_id:"user:someone-else",display_name:"Alice",kind:"human"}}} self="me-123" mayEdit onAction={vi.fn()}/>)
  expect(screen.getByText(/Upravuje Alice/)).toBeInTheDocument()
  expect(screen.getByRole("button",{name:"Požádat o předání"})).toBeInTheDocument()
  expect(screen.queryByRole("status")).not.toBeInTheDocument()
 })
})
