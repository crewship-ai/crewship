import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { CraftProposal } from "../../projects/types"

const state = vi.hoisted(() => ({
  accept: { mutate: (() => {}) as (id: string, o?: { onSuccess?: () => void }) => void, isPending: false, isError: false },
  reject: { mutate: (() => {}) as (id: string, o?: { onSuccess?: () => void }) => void, isPending: false, isError: false },
}))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock("sonner", () => ({ toast }))
vi.mock("../use-craft-lease", () => ({
  useCraftProposalActions: () => ({ accept: state.accept, reject: state.reject }),
}))

import { CraftProposalCard } from "../craft-proposal-card"

const proposal = (over: Partial<CraftProposal> = {}): CraftProposal => ({
  id: "p1", author: { actor_id: "a1", display_name: "Writer", kind: "agent" }, base_version: 4,
  summary: "Tightened the intro", created_at: "2026-10-27T10:00:00Z", state: "open", ...over,
})

beforeEach(() => {
  state.accept = { mutate: vi.fn((_id, o) => o?.onSuccess?.()), isPending: false, isError: false }
  state.reject = { mutate: vi.fn((_id, o) => o?.onSuccess?.()), isPending: false, isError: false }
  toast.success.mockReset()
})
afterEach(cleanup)

it("shows an open proposal with compare, accept and reject", () => {
  const onCompare = vi.fn()
  const p = proposal()
  render(<CraftProposalCard workspaceId="w" slug="doc" proposal={p} onCompare={onCompare} />)
  expect(screen.getByText("Writer prepared a proposal on v4")).toBeTruthy()
  expect(screen.getByText("Tightened the intro · the file stays as it is until you accept")).toBeTruthy()
  const names = screen.getAllByRole("button").map(b => b.textContent)
  expect(names).toEqual(["Compare", "Reject", "Accept"])
  fireEvent.click(screen.getByRole("button", { name: "Compare" }))
  expect(onCompare).toHaveBeenCalledWith(p)
  fireEvent.click(screen.getByRole("button", { name: "Accept" }))
  expect(state.accept.mutate).toHaveBeenCalledWith("p1", expect.anything())
  expect(toast.success).toHaveBeenCalledWith("Accepted · saved as a new version after v4")
  fireEvent.click(screen.getByRole("button", { name: "Reject" }))
  expect(state.reject.mutate).toHaveBeenCalledWith("p1", expect.anything())
  expect(toast.success).toHaveBeenCalledWith("Proposal rejected · Writer was told")
})

it("falls back to the guarantee line without a summary", () => {
  render(<CraftProposalCard workspaceId="w" slug="doc" proposal={proposal({ summary: "" })} onCompare={() => {}} />)
  expect(screen.getByText("The file stays as it is until you accept")).toBeTruthy()
})

it("disables both decisions while one is pending", () => {
  state.accept = { mutate: vi.fn(), isPending: true, isError: false }
  render(<CraftProposalCard workspaceId="w" slug="doc" proposal={proposal()} onCompare={() => {}} />)
  expect((screen.getByRole("button", { name: "Accept" }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByRole("button", { name: "Reject" }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByRole("button", { name: "Compare" }) as HTMLButtonElement).disabled).toBe(false)
})

it("offers only Compare on a stale proposal", () => {
  render(<CraftProposalCard workspaceId="w" slug="doc" proposal={proposal({ state: "stale" })} onCompare={() => {}} />)
  expect(screen.getByText("Out of date — the document changed")).toBeTruthy()
  expect(screen.getByRole("button", { name: "Compare" })).toBeTruthy()
  expect(screen.queryByRole("button", { name: "Accept" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Reject" })).toBeNull()
})

it("collapses resolved proposals to one line", () => {
  const { rerender } = render(<CraftProposalCard workspaceId="w" slug="doc" proposal={proposal({ state: "accepted" })} onCompare={() => {}} />)
  expect(screen.getByText("Writer's proposal was accepted")).toBeTruthy()
  expect(screen.queryByRole("button")).toBeNull()
  rerender(<CraftProposalCard workspaceId="w" slug="doc" proposal={proposal({ state: "rejected" })} onCompare={() => {}} />)
  expect(screen.getByText("Writer's proposal was rejected")).toBeTruthy()
})
