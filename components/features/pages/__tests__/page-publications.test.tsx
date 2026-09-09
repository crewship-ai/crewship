import React from "react"
import { afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
const state = vi.hoisted(() => ({
  query: { data: { pages: [{ publication_version: 5, published: true, can_publish: true, publications: [{ version: 2, source_revision: 3, build_id: "b", git_commit: "abc", artifact_digest: "sha", created_at: "2026-09-09", rollback_of: 0, withdrawn_at: "" }] }] }, isError: false, isPending: false },
  source: { data: { git_commit: "abc", project: { files: [{ path: "src/main.tsx", encoding: "utf8", content: "export const title = 'MySQL';" }] } }, isError: false, isPending: false },
  withdraw: { mutate: vi.fn(), isPending: false }, publish: { mutate: vi.fn(), isPending: false },
}))
vi.mock("@/hooks/use-page-publications", () => ({ usePagePublications: () => state }))
vi.mock("@/hooks/use-page-application", () => ({ usePageApplication: () => ({ publish: state.publish }) }))
import { PagePublicationsDialog } from "../page-publications"
afterEach(() => { cleanup(); state.publish.mutate.mockReset(); state.withdraw.mutate.mockReset(); state.query.data.pages[0].publication_version = 5; state.query.data.pages[0].can_publish = true })
it("requires review of inspected source and fences rollback to the reviewed current publication", () => {
  const view = render(<PagePublicationsDialog workspaceId="ws" slug="health" onClose={vi.fn()} />)
  fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
  expect(screen.getByText("export const title = 'MySQL';")).toBeTruthy()
  const restore = screen.getByRole("button", { name: "Restore version 2" }) as HTMLButtonElement
  expect(restore.disabled).toBe(true)
  fireEvent.click(screen.getByLabelText("I reviewed version 2 and trust its code."))
  fireEvent.click(restore)
  expect(state.publish.mutate).toHaveBeenCalledWith({ rollback_version: 2, expected_publication: 5, reviewed_code: true })
  state.query.data.pages[0].publication_version = 6
  view.rerender(<PagePublicationsDialog workspaceId="ws" slug="health" onClose={vi.fn()} />)
  expect(restore.disabled).toBe(true)
})
it("requires a fresh withdrawal confirmation if another user publishes", () => {
  const view = render(<PagePublicationsDialog workspaceId="ws" slug="health" onClose={vi.fn()} />)
  const withdraw = screen.getByRole("button", { name: "Withdraw application" }) as HTMLButtonElement
  expect(withdraw.disabled).toBe(true)
  fireEvent.click(screen.getByLabelText("Stop this application for all viewers"))
  state.query.data.pages[0].publication_version = 6
  view.rerender(<PagePublicationsDialog workspaceId="ws" slug="health" onClose={vi.fn()} />)
  expect(withdraw.disabled).toBe(true)
  fireEvent.click(screen.getByLabelText("Stop this application for all viewers"))
  fireEvent.click(withdraw)
  expect(state.withdraw.mutate).toHaveBeenCalledWith(6)
})
it("does not offer publication control to an editor without publishing authority", () => {
  state.query.data.pages[0].can_publish = false
  render(<PagePublicationsDialog workspaceId="ws" slug="health" onClose={vi.fn()} />)
  fireEvent.click(screen.getByRole("button", { name: "Inspect version 2" }))
  expect(screen.queryByRole("button", { name: "Restore version 2" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Withdraw application" })).toBeNull()
})
