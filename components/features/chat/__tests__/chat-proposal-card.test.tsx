import React, { useState } from "react"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import fixture from "@/internal/api/testdata/chat-e2/proposal-draft.json"
import { ProposalRefSchema } from "@/lib/chat-e2-cards"
import { proposalRecoveryKey } from "@/hooks/use-proposal-draft"
import { ChatProposalCard } from "../chat-proposal-card"

const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
const base = ProposalRefSchema.parse(fixture)
const key = proposalRecoveryKey({ workspaceId: base.entity.workspace_id, viewerId: "viewer", proposalId: base.entity.id, surface: "card" }, "discard")
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
const lost = vi.fn()
function setup() {
  function Harness() {
    const [reference, setReference] = useState(base)
    return <ChatProposalCard reference={reference} chatId="chat" userId="viewer" surface="card" rechecking={false}
      assignees={[]} onSnapshot={setReference} onAccessLost={lost} onComment={async () => false} renderIssue={() => null} />
  }
  const client = new QueryClient()
  return render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>)
}
afterEach(() => { cleanup(); fetch.mockReset(); lost.mockReset(); sessionStorage.clear() })

it("uses the real API intent/key and explicitly reads current state to recover a key conflict", async () => {
  fetch.mockResolvedValueOnce(response({ error: "operation_key_conflict" }, 409))
    .mockResolvedValueOnce(response({ ...base, proposal: { ...base.proposal, version: 2 } }))
    .mockResolvedValueOnce(response({ ...base, proposal: { ...base.proposal, version: 3, status: "discarded", allowed_actions: ["view"] } }))
  setup()
  fireEvent.click(screen.getByRole("button", { name: "Discard proposal" }))
  fireEvent.click(await screen.findByRole("button", { name: "Read current proposal and review conflict" }))
  await screen.findByText(/A newer proposal version/)
  expect(sessionStorage.getItem(key)).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Use current proposal and replace field draft" }))
  fireEvent.click(screen.getByRole("button", { name: "Discard proposal" }))
  await screen.findByText("This proposal is discarded.")
  expect(fetch.mock.calls[0][0]).toContain(`/proposals/${base.entity.id}/discard?workspace_id=`)
  expect(fetch.mock.calls[0][1].method).toBe("POST")
  expect(fetch.mock.calls[1][1]).toMatchObject({ cache: "no-store" })
  expect(fetch.mock.calls[0][1].headers["Idempotency-Key"]).not.toBe(fetch.mock.calls[2][1].headers["Idempotency-Key"])
  expect(JSON.parse(fetch.mock.calls[2][1].body)).toEqual({ expected_version: 2 })
})

it.each([500, 403])("keeps conflict intent until an authorized current snapshot is read (%s)", async status => {
  fetch.mockResolvedValueOnce(response({ error: "operation_key_conflict" }, 409))
    .mockResolvedValueOnce(response({}, status))
  setup()
  fireEvent.click(screen.getByRole("button", { name: "Discard proposal" }))
  fireEvent.click(await screen.findByRole("button", { name: "Read current proposal and review conflict" }))
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2))
  if (status === 403) await waitFor(() => expect(lost).toHaveBeenCalledOnce())
  else expect(sessionStorage.getItem(key)).not.toBeNull()
  expect(screen.queryByText("This proposal is discarded.")).toBeNull()
})
