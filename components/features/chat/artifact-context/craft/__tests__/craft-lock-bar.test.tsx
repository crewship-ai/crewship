import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { CraftLeaseStatus } from "../../projects/types"

const push = vi.fn()
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }))

const state = vi.hoisted(() => ({
  available: true,
  lease: null as unknown,
  acquire: { mutate: (() => {}) as (v: unknown, o?: { onSuccess?: () => void }) => void, isPending: false, isError: false },
  requestHandoff: { mutate: (() => {}) as (v: unknown, o?: { onSuccess?: () => void }) => void, isPending: false, isError: false },
}))
vi.mock("../use-craft-lease", () => ({
  useCraftAvailable: () => state.available,
  useCraftLease: () => ({ data: state.lease }),
  useCraftLeaseActions: () => ({ acquire: state.acquire, requestHandoff: state.requestHandoff }),
}))

import { CraftLockBar } from "../craft-lock-bar"
import { CraftPreview } from "../craft-preview"
import { CraftStateChip } from "../craft-state-chip"

const lease = (over: Partial<CraftLeaseStatus> = {}): CraftLeaseStatus => ({ holder: null, mine: false, generation: 1, can_write: true, ...over })
const pavel = { actor_id: "u2", display_name: "Pavel", kind: "human" as const }

beforeEach(() => {
  state.available = true
  state.lease = lease()
  state.acquire = { mutate: vi.fn((_v, o) => o?.onSuccess?.()), isPending: false, isError: false }
  state.requestHandoff = { mutate: vi.fn((_v, o) => o?.onSuccess?.()), isPending: false, isError: false }
})
afterEach(() => { cleanup(); push.mockReset() })

it("renders nothing when Craft is unavailable", () => {
  state.available = false
  const { container } = render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(container.innerHTML).toBe("")
})

it("offers Edit on a free document, acquires the lease, then opens the editor", () => {
  const onEdit = vi.fn()
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={onEdit} />)
  expect(screen.getByText("Free")).toBeTruthy()
  expect(screen.getByText("nobody is editing")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  expect(state.acquire.mutate).toHaveBeenCalledOnce()
  expect(onEdit).toHaveBeenCalledOnce()
})

it("does not open the editor when acquiring fails", () => {
  state.acquire = { mutate: vi.fn(), isPending: false, isError: true }
  const onEdit = vi.fn()
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={onEdit} />)
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  expect(onEdit).not.toHaveBeenCalled()
  expect(screen.getByText("Could not start editing")).toBeTruthy()
})

it("shows my own lease with its generation and an Open in new tab link, and no Release without an API", () => {
  state.lease = lease({ holder: { actor_id: "me", display_name: "Me", kind: "human" }, mine: true, generation: 18 })
  render(<CraftLockBar workspaceId="w 1" slug="q&a" onEdit={() => {}} />)
  expect(screen.getByText("Editing: you")).toBeTruthy()
  expect(screen.getByText("generation 18 · held while this tab stays connected")).toBeTruthy()
  const link = screen.getByRole("link", { name: "Open in new tab" })
  expect(link.getAttribute("href")).toBe("/pages/q%26a?workspace_id=w+1")
  expect(link.getAttribute("target")).toBe("_blank")
  expect(screen.queryByRole("button", { name: "Release" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Save version" })).toBeNull()
  expect(state.acquire.mutate).not.toHaveBeenCalled()
})

it("names another human holder and requests an edit handoff", () => {
  state.lease = lease({ holder: pavel })
  render(<CraftLockBar workspaceId="w" slug="doc" onEdit={() => {}} />)
  expect(screen.getByText("Editing: Pavel")).toBeTruthy()
  expect(screen.getByText("you see a read-only preview")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Request edit" }))
  expect(state.requestHandoff.mutate).toHaveBeenCalledOnce()
  expect(screen.getByText("Request sent to Pavel · they can hand over or decline")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Request edit" })).toBeNull()
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

it("preview shows the engine, routes Edit to the page editor, and shows the handoff banner when someone waits on my lease", () => {
  render(<CraftPreview workspaceId="w 1" slug="q&a" kind="word" title="Plan"><p>body</p></CraftPreview>)
  expect(screen.getByText("WordCraft")).toBeTruthy()
  expect(screen.getByText("Plan")).toBeTruthy()
  expect(screen.getByText("body")).toBeTruthy()
  expect(screen.queryByText(/is waiting for handoff/)).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  expect(push).toHaveBeenCalledWith("/pages/q%26a?workspace_id=w+1")
  cleanup()
  state.lease = lease({ holder: { actor_id: "me", display_name: "Me", kind: "human" }, mine: true, handoff_requested_by: "Scout" })
  render(<CraftPreview workspaceId="w" slug="doc" kind="sheet" title="Model"><p>grid</p></CraftPreview>)
  expect(screen.getByText("GridCraft")).toBeTruthy()
  expect(screen.getByText("Scout is waiting for handoff")).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Hand over" })).toBeNull()
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
