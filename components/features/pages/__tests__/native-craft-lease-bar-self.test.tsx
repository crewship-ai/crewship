import {render,screen,act} from "@testing-library/react"
import {describe,it,expect,vi,afterEach} from "vitest"
import {NativeCraftLeaseBar,type NativeCraftLeaseView} from "../native-craft-lease-bar"

// UX walkthrough RC-4, R06: after a reload the previous tab's editor session
// still holds the lease until its 15 s TTL lapses. The bar said
// "<me> is editing · Request edit" — asking oneself, with no hint that it
// frees by itself.
const now=Date.parse("2026-11-04T10:00:00Z")
const previousTab:NativeCraftLeaseView={holder:{actor_id:"user:me-123",display_name:"PRD QA owner",kind:"human"},mine:false,generation:"7",can_write:false,acquired_at:null,expires_at:new Date(now+12_400).toISOString(),handoff_requested_by:[]}

afterEach(()=>{vi.useRealTimers()})

describe("lease held by the same person in another session",()=>{
 it("says it is the person's other tab and when a closed one frees the lease",()=>{
  vi.useFakeTimers({now})
  render(<NativeCraftLeaseBar view={previousTab} self="me-123" mayEdit onAction={vi.fn()}/>)
  expect(screen.getByText("You are editing in another tab or window")).toBeInTheDocument()
  expect(screen.queryByText(/PRD QA owner is editing/)).not.toBeInTheDocument()
  expect(screen.getByRole("status")).toHaveTextContent("editing is released within 13 s")
  act(()=>{vi.advanceTimersByTime(5_000)})
  expect(screen.getByRole("status")).toHaveTextContent("editing is released within 8 s")
  act(()=>{vi.advanceTimersByTime(10_000)})
  expect(screen.getByRole("status")).toHaveTextContent("Waiting for the other tab to release editing")
  expect(screen.getByRole("button",{name:"Ask the other tab to hand over"})).toBeInTheDocument()
 })

 it("keeps the ordinary holder wording for another person",()=>{
  render(<NativeCraftLeaseBar view={{...previousTab,holder:{actor_id:"user:someone-else",display_name:"Alice",kind:"human"}}} self="me-123" mayEdit onAction={vi.fn()}/>)
  expect(screen.getByText(/Alice is editing/)).toBeInTheDocument()
  expect(screen.getByRole("button",{name:"Request edit"})).toBeInTheDocument()
  expect(screen.queryByRole("status")).not.toBeInTheDocument()
 })
})
