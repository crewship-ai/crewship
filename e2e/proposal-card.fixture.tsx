import React, { useState } from "react"
import { createRoot } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ProposalEntityCard } from "../components/features/chat/proposal-entity-card"
import { ProposalRefSchema, type ProposalRef } from "../lib/chat-e2-cards"
import fixture from "../internal/api/testdata/chat-e2/proposal-draft.json"
const base = ProposalRefSchema.parse(fixture)
function App() {
  const [reference, setReference] = useState(base)
  const [unavailable, setUnavailable] = useState(false)
  return <main style={{ maxWidth: 600, padding: 12 }}>
    <button onClick={() => setReference(previous => ({ ...previous, proposal: { ...previous.proposal, version: previous.proposal.version + 1, title: "New agent proposal" } }))}>Simulate agent version</button>
    {!unavailable && <ProposalEntityCard reference={reference} viewerId="viewer" surface="card" assignees={[{ id: "agent-1", name: "Jamie" }]} onSnapshot={setReference} onAccessLost={() => setUnavailable(true)} onComment={async comment => { await fetch("/comment", { method: "POST", body: comment }); return true }} renderIssue={issue => <p>Current issue {issue.identifier}</p>} transport={request => fetch("/operation", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request) })} />}
  </main>
}
createRoot(document.getElementById("root")!).render(<QueryClientProvider client={new QueryClient()}><App /></QueryClientProvider>)
export type { ProposalRef }
