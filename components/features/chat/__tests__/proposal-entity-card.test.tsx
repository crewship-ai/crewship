import React, { useState } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import fixture from "@/internal/api/testdata/chat-e2/proposal-draft.json"
import { ProposalRefSchema, type ProposalRef } from "@/lib/chat-e2-cards"
import { proposalRecoveryKey } from "@/hooks/use-proposal-draft"
import type { ProposalRequest } from "@/hooks/use-proposal-operation"
import { ProposalEntityCard } from "../proposal-entity-card"
const base = ProposalRefSchema.parse(fixture)
const scope = { workspaceId: base.entity.workspace_id, viewerId: "viewer", proposalId: base.entity.id, surface: "card" as const }
const transport = vi.fn<(request: ProposalRequest) => Promise<Response>>()
const onComment = vi.fn(async () => true)
const onAccessLost = vi.fn()
const snapshot = (value: ProposalRef) => new Response(JSON.stringify(value))
function setup(initial = base, surface: "card" | "panel" = "card") {
  let change: (reference: ProposalRef) => void = () => {}
  let setChecking: (checking: boolean) => void = () => {}
  function Harness() {
    const [reference, setReference] = useState(initial)
    const [rechecking, changeChecking] = useState(false)
    setChecking = changeChecking
    change = setReference
    return <ProposalEntityCard rechecking={rechecking} reference={reference} viewerId="viewer" surface={surface} transport={transport} onSnapshot={setReference} onAccessLost={onAccessLost} onComment={onComment} assignees={[{ id: "agent-1", name: "Jamie" }]} renderIssue={issue => <p>Issue card {issue.identifier}</p>} />
  }
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const view = render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>)
  return { ...view, setChecking: (checking: boolean) => setChecking(checking), change: (reference: ProposalRef) => { change(reference) } }
}
afterEach(() => { cleanup(); sessionStorage.clear(); transport.mockReset(); onComment.mockReset().mockResolvedValue(true); onAccessLost.mockReset() })
it("edits/selects bounded issues, saves before explicit count/effect/actor confirmation, then renders immutable receipt and E1 slots", async () => {
  transport.mockImplementation(async request => {
    if (request.action === "edit") { const body = JSON.parse(request.body); return snapshot({ ...base, proposal: { ...base.proposal, ...body, expected_version: undefined, version: 2 } } as ProposalRef) }
    return snapshot({ ...base, proposal: { ...base.proposal, version: 2, status: "accepted", allowed_actions: ["view"], items: base.proposal.items.map((item, index) => ({ ...item, selected: index === 0 })), outcome: { reviewed_version: 2, confirmed_by: "viewer", confirmed_at: "2026-10-16T10:00:00Z", issues: [{ id: "issue-1", identifier: "ENG-1" }] } } })
  })
  setup()
  fireEvent.change(screen.getByLabelText("Issue 1 title"), { target: { value: "Measure latency" } })
  fireEvent.change(screen.getByLabelText("Issue 1 priority"), { target: { value: "urgent" } })
  fireEvent.change(screen.getByLabelText("Issue 1 assignee"), { target: { value: "agent-1" } })
  fireEvent.click(screen.getByLabelText("Include issue 2"))
  expect(screen.getByRole("button", { name: "Review confirmation" })).toBeDisabled()
  expect(transport).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Save proposal edits" }))
  await waitFor(() => expect(screen.getByRole("button", { name: "Review confirmation" })).toBeEnabled())
  expect(JSON.parse(transport.mock.calls[0][0].body).items[0]).toMatchObject({ title: "Measure latency", priority: "urgent", assignee_id: "agent-1" })
  fireEvent.click(screen.getByRole("button", { name: "Review confirmation" }))
  expect(screen.getByText("Create 1 issues as viewer.")).toBeVisible()
  expect(screen.getByText("All selected issues are created together. Direct start is a separate action; existing workspace automations still apply.")).toBeVisible()
  fireEvent.click(screen.getByRole("button", { name: "Confirm creation of 1 issues" }))
  await screen.findByText("Issue card ENG-1")
  expect(screen.queryByRole("textbox")).toBeNull()
  expect(JSON.parse(transport.mock.calls[1][0].body)).toEqual({ proposal_id: base.entity.id, reviewed_version: 2 })
})
it("keeps independent field/comment drafts and focus through source refresh and reload, requiring explicit conflict review", async () => {
  const view = setup()
  const input = screen.getByLabelText("Issue 1 title")
  input.focus()
  fireEvent.change(input, { target: { value: "Human draft" } })
  fireEvent.change(screen.getByLabelText("Comment to agent"), { target: { value: "Investigate first" } })
  act(() => view.change({ ...base, proposal: { ...base.proposal, version: 2, title: "Agent revision" } }))
  await screen.findByText(/A newer proposal version/)
  expect(screen.getByLabelText("Issue 1 title")).toBe(input)
  expect(input).toHaveValue("Human draft")
  expect(document.activeElement).toBe(input)
  view.unmount(); setup({ ...base, proposal: { ...base.proposal, version: 2 } })
  expect(screen.getByLabelText("Issue 1 title")).toHaveValue("Human draft")
  expect(screen.getByLabelText("Comment to agent")).toHaveValue("Investigate first")
  fireEvent.click(screen.getByRole("button", { name: "Use current proposal and replace field draft" }))
  expect(screen.getByLabelText("Issue 1 title")).toHaveValue("Profile API")
  expect(screen.getByLabelText("Comment to agent")).toHaveValue("Investigate first")
})
it("keeps exact body/key on unknown 500 across reload and retry", async () => {
  transport.mockResolvedValueOnce(new Response("{}", { status: 500 }))
  const view = setup()
  fireEvent.click(screen.getByRole("button", { name: "Review confirmation" }))
  fireEvent.click(screen.getByRole("button", { name: "Confirm creation of 2 issues" }))
  await screen.findByRole("button", { name: "Retry identical operation" })
  const request = transport.mock.calls[0][0]
  view.unmount(); setup()
  expect(screen.getByRole("button", { name: "Review confirmation" })).toBeDisabled()
  transport.mockResolvedValueOnce(snapshot(base))
  fireEvent.click(screen.getByRole("button", { name: "Retry identical operation" }))
  await waitFor(() => expect(transport).toHaveBeenCalledTimes(2))
  expect(transport.mock.calls[1][0]).toEqual(request)
})
it.each([401, 403, 404])("purges both surfaces on explicit %s access loss", async status => {
  sessionStorage.setItem(proposalRecoveryKey({ ...scope, surface: "panel" }, "comment"), "Private panel draft")
  transport.mockResolvedValueOnce(new Response("{}", { status }))
  setup()
  fireEvent.change(screen.getByLabelText("Comment to agent"), { target: { value: "Private comment" } })
  fireEvent.click(screen.getByRole("button", { name: "Discard proposal" }))
  await waitFor(() => expect(onAccessLost).toHaveBeenCalledOnce())
  expect(screen.queryByText(base.proposal.title)).toBeNull()
  expect(sessionStorage.length).toBe(0)
})
it("isolates panel drafts from card, workspace, viewer, source and action", () => {
  sessionStorage.setItem(proposalRecoveryKey(scope, "comment"), "Card comment")
  sessionStorage.setItem(proposalRecoveryKey({ ...scope, viewerId: "other" }, "fields"), JSON.stringify({ ...base.proposal, title: "Other viewer" }))
  setup(base, "panel")
  expect(screen.getByLabelText("Comment to agent")).toHaveValue("")
  expect(screen.getByLabelText("Proposal title")).toHaveValue(base.proposal.title)
})
it("validation preserves edits; conflict snapshot cannot silently replace them", async () => {
  transport.mockResolvedValueOnce(new Response("{}", { status: 422 }))
  setup()
  fireEvent.change(screen.getByLabelText("Issue 1 title"), { target: { value: "Human draft" } })
  fireEvent.click(screen.getByRole("button", { name: "Save proposal edits" }))
  await screen.findByText("Check the proposal fields and try again.")
  expect(screen.getByLabelText("Issue 1 title")).toHaveValue("Human draft")
  transport.mockResolvedValueOnce(new Response(JSON.stringify({ error: "proposal_version_conflict", current: { ...base, proposal: { ...base.proposal, version: 2 } } }), { status: 409 }))
  fireEvent.click(screen.getByRole("button", { name: "Save proposal edits" }))
  await screen.findByRole("button", { name: "Review current version; keep draft" })
  expect(screen.getByLabelText("Issue 1 title")).toHaveValue("Human draft")
  fireEvent.click(screen.getByRole("button", { name: "Review current version; keep draft" }))
  await screen.findByText(/A newer proposal version/)
  expect(screen.getByLabelText("Issue 1 title")).toHaveValue("Human draft")
})
it("sends a comment only explicitly and keeps its draft when send fails", async () => {
  onComment.mockResolvedValueOnce(false)
  setup()
  fireEvent.change(screen.getByLabelText("Comment to agent"), { target: { value: "Explain the tradeoff" } })
  fireEvent.change(screen.getByLabelText("Issue 1 title"), { target: { value: "Unrelated field draft" } })
  expect(onComment).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Send comment to agent" }))
  await screen.findByText("Could not send. Your comment draft is kept.")
  expect(screen.getByLabelText("Comment to agent")).toHaveValue("Explain the tradeoff")
  expect(screen.getByLabelText("Issue 1 title")).toHaveValue("Unrelated field draft")
})
it("does not send an operation if durable recovery is unavailable", async () => {
  setup()
  const storage = vi.spyOn(sessionStorage, "setItem").mockImplementation(() => { throw new Error("storage disabled") })
  fireEvent.click(screen.getByRole("button", { name: "Discard proposal" }))
  await screen.findByText("Recovery storage is unavailable. No request was sent.")
  expect(transport).not.toHaveBeenCalled()
  storage.mockRestore()
})

it("does not retry an uncertain write until current access is verified", async () => {
  transport.mockResolvedValueOnce(new Response("{}", { status: 500 }))
  const view = setup()
  fireEvent.click(screen.getByRole("button", { name: "Discard proposal" }))
  const retry = await screen.findByRole("button", { name: "Retry identical operation" })
  act(() => view.setChecking(true))
  expect(retry).toBeDisabled()
  fireEvent.click(retry)
  expect(transport).toHaveBeenCalledTimes(1)
  act(() => view.setChecking(false))
  expect(retry).toBeEnabled()
})

it("purges only the old source after a late denial without calling a new surface callback", async () => {
  let finish: (response: Response) => void = () => {}
  transport.mockImplementation(() => new Promise(resolve => { finish = resolve }))
  const view = setup()
  fireEvent.change(screen.getByLabelText("Issue 1 title"), { target: { value: "Pending edit" } })
  fireEvent.click(screen.getByRole("button", { name: "Save proposal edits" }))
  await waitFor(() => expect(transport).toHaveBeenCalledTimes(1))
  expect(sessionStorage.getItem(proposalRecoveryKey(scope, "edit"))).not.toBeNull()
  view.unmount()
  await act(async () => finish(new Response("{}", { status: 403 })))
  expect(sessionStorage.getItem(proposalRecoveryKey(scope, "edit"))).toBeNull()
  expect(onAccessLost).not.toHaveBeenCalled()
})
