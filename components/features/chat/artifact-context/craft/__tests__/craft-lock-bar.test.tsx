import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { CraftLeaseStatus } from "../../projects/types"

const push = vi.fn()
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }))

const state = vi.hoisted(() => ({
  available: true,
  lease: null as unknown,
  heldByMe: false,
  requestedByMe: false,
  requestedGeneration: null as string | null,
  requestHandoff: { mutate: (() => {}) as (v: unknown) => void, isPending: false, isError: false },
}))
vi.mock("../use-craft-lease", () => ({
  useCraftAvailable: () => state.available,
  useCraftLeaseState: () => ({
    status: state.available ? state.lease : undefined, requestHandoff: state.requestHandoff,
    requestedGeneration: state.requestedGeneration, heldByMe: state.heldByMe, requestedByMe: state.requestedByMe,
  }),
}))

import { CraftLockBar } from "../craft-lock-bar"
import { CraftPreview } from "../craft-preview"
import { CraftStateChip } from "../craft-state-chip"

const lease = (over: Partial<CraftLeaseStatus> = {}): CraftLeaseStatus => ({ holder: null, mine: false, generation: "1", can_write: true, requests: [], ...over })
const pavel = { actor_id: "user:u2", display_name: "Pavel", kind: "human" as const }

beforeEach(() => {
  state.available = true
  state.lease = lease()
  state.heldByMe = false
  state.requestedByMe = false
  state.requestedGeneration = null
  state.requestHandoff = { mutate: vi.fn(() => { state.requestedGeneration = (state.lease as CraftLeaseStatus).generation }), isPending: false, isError: false }
})
afterEach(() => { cleanup(); push.mockReset() })

it("renders nothing when Craft is unavailable", () => {
  state.available = false
  const { container } = render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(container.innerHTML).toBe("")
})

it("offers Edit on a free document and only opens the editor, which takes the lease itself", () => {
  const onEdit = vi.fn()
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={onEdit} />)
  expect(screen.getByText("Free")).toBeTruthy()
  expect(screen.getByText("nobody is editing")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  expect(onEdit).toHaveBeenCalledOnce()
})

it("recognises my own hold in the editor by account, with an Open in new tab link and no Release", () => {
  state.lease = lease({ holder: { actor_id: "user:me", display_name: "Me", kind: "human" }, generation: "18" })
  state.heldByMe = true
  render(<CraftLockBar workspaceId="w 1" slug="q&a" onEdit={() => {}} />)
  expect(screen.getByText("Editing: you")).toBeTruthy()
  expect(screen.getByText("in the editor")).toBeTruthy()
  const link = screen.getByRole("link", { name: "Open in new tab" })
  expect(link.getAttribute("href")).toBe("/pages/q%26a?workspace_id=w+1")
  expect(link.getAttribute("target")).toBe("_blank")
  expect(screen.queryByRole("button", { name: "Release" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Request edit" })).toBeNull()
})

it("names another human holder and requests an edit handoff", () => {
  state.lease = lease({ holder: pavel })
  const { rerender } = render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByText("Editing: Pavel")).toBeTruthy()
  expect(screen.getByText("you see a read-only preview")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Request edit" }))
  expect(state.requestHandoff.mutate).toHaveBeenCalledOnce()
  rerender(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByText("Request sent to Pavel · they can hand over or decline")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Request edit" })).toBeNull()
})

it("a request made for an earlier generation does not count for a new lease", () => {
  state.lease = lease({ holder: pavel, generation: "5" })
  state.requestedGeneration = "4"
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByRole("button", { name: "Request edit" })).toBeTruthy()
})

it("keeps showing my pending request after a reload, from the lease itself", () => {
  state.lease = lease({ holder: pavel })
  state.requestedByMe = true
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByText("Request sent to Pavel · they can hand over or decline")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Request edit" })).toBeNull()
})

it("says so when the request fails", () => {
  state.lease = lease({ holder: pavel })
  state.requestHandoff = { mutate: vi.fn(), isPending: false, isError: true }
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByText("Could not request edit")).toBeTruthy()
})

it("labels an agent holder", () => {
  state.lease = lease({ holder: { actor_id: "a1", display_name: "Writer", kind: "agent" } })
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByText("Editing: Writer (agent)")).toBeTruthy()
})

it("is read only without write access", () => {
  state.lease = lease({ can_write: false, holder: pavel })
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByText("Read only")).toBeTruthy()
  expect(screen.queryByRole("button")).toBeNull()
})

it("announces holder changes politely", () => {
  state.lease = lease({ holder: pavel })
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByRole("status").getAttribute("aria-live")).toBe("polite")
})

it("preview shows the engine and routes Edit to the page editor; no handoff banner, chat never holds the lease", () => {
  render(<CraftPreview workspaceId="w 1" slug="q&a" kind="word" title="Plan"><p>body</p></CraftPreview>)
  expect(screen.getByText("WordCraft")).toBeTruthy()
  expect(screen.getByText("Plan")).toBeTruthy()
  expect(screen.getByText("body")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  expect(push).toHaveBeenCalledWith("/pages/q%26a?workspace_id=w+1")
  cleanup()
  render(<CraftPreview workspaceId="w" slug="doc" kind="sheet" title="Model"><p>grid</p></CraftPreview>)
  expect(screen.getByText("GridCraft")).toBeTruthy()
  expect(screen.queryByText(/is waiting for handoff/)).toBeNull()
})

it("state chip reads Editing: name with a lock, or Free to edit", () => {
  const { rerender, container } = render(<CraftStateChip holder={pavel} mine={false} />)
  expect(screen.getByText("Editing: Pavel")).toBeTruthy()
  expect(container.querySelector("svg")).toBeTruthy()
  rerender(<CraftStateChip holder={null} mine={false} />)
  expect(screen.getByText("Free to edit")).toBeTruthy()
  rerender(<CraftStateChip holder={pavel} mine />)
  expect(screen.getByText("Editing: you")).toBeTruthy()
})
