import React from "react"
import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
const state = vi.hoisted(() => ({ query: { data: { pages: [{ revisions: [3,2,1].map(revision => ({ revision, git_commit: "abc", created_at: "2026-09-08", restorable: revision !== 1 })) }] }, isError: false }, restore: { mutate: vi.fn(), isPending: false } }))
vi.mock("@/hooks/use-page-project-history", () => ({ usePageProjectHistory: () => state }))
import { PageProjectHistoryDialog } from "../page-project-history"
afterEach(() => { cleanup(); state.restore.mutate.mockReset() })
it("restores only an available older draft using the displayed current revision as CAS", () => {
  render(<PageProjectHistoryDialog workspaceId="ws" slug="health" onClose={vi.fn()} />)
  expect((screen.getByRole("button", { name: "Restore draft 3" }) as HTMLButtonElement).disabled).toBe(true)
  expect((screen.getByRole("button", { name: "Restore draft 1" }) as HTMLButtonElement).disabled).toBe(true)
  fireEvent.click(screen.getByRole("button", { name: "Restore draft 2" }))
  expect(state.restore.mutate).toHaveBeenCalledWith({ revision: 2, expectedRevision: 3 })
})
