import { afterEach, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { ApprovalDetail } from "../approval-detail"
import { routineDraftReviewLink } from "@/lib/routine-draft-review-link"
import type { ApprovalRow } from "@/lib/types/approvals"

vi.mock("@/hooks/use-approvals", () => ({ decideApproval: vi.fn() }))
const row: ApprovalRow = { id: "review1", kind: "routine_draft_review", status: "pending", reason: "Review this draft", created_at: "2026-10-15T00:00:00Z", payload: { slug: "shared", draft_id: "draft1", draft_revision: 3 } }
afterEach(cleanup)

it("offers exact-draft publication review and denial, without a generic approve action", () => {
  render(<ApprovalDetail row={row} open onOpenChange={() => {}} onDecided={() => {}} workspaceId="w" />)
  expect(screen.queryByRole("button", { name: "Approve" })).toBeNull()
  expect(screen.getByRole("button", { name: "Deny" })).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Review draft for publication" })).toHaveAttribute("href", "/routines?slug=shared&view=publish&draft_review=draft1%3A3")
})

it("does not link a malformed request to an unrestricted latest draft", () => {
  render(<ApprovalDetail row={{ ...row, payload: { slug: "shared" } }} open onOpenChange={() => {}} onDecided={() => {}} />)
  expect(screen.queryByRole("link", { name: "Review draft for publication" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Approve" })).toBeNull()
  expect(screen.getByRole("alert")).toHaveTextContent("The requested draft reference is unavailable.")
})

it("encodes the slug and snapshot independently and rejects non-integral revisions", () => {
  expect(routineDraftReviewLink({ slug: "shared&view=edit", draft_id: "d:other", draft_revision: 3 })).toBe("/routines?slug=shared%26view%3Dedit&view=publish&draft_review=d%3Aother%3A3")
  expect(routineDraftReviewLink({ slug: "shared", draft_id: "d", draft_revision: 1.5 })).toBeNull()
})
