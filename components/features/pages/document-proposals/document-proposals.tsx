"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import type { DocumentProposal, DocumentProposalClient, DocumentProposalDetail } from "@/lib/documents/proposals"
import { ProposalReview } from "./proposal-review"

type View = { status: "loading" | "error" } | { status: "ready"; items: DocumentProposal[] }
type Detail = { status: "loading" | "error" } | { status: "ready"; value: DocumentProposalDetail; reviewedRev: number }

/** Lists proposals for one Document and opens one at a time. Needs the proposals API (flagged). */
export function DocumentProposals({ client, headRev, canDecide, hasLocalDraft, onDownloadDraft, onAccepted }: {
  client: DocumentProposalClient
  headRev: number
  canDecide: boolean
  hasLocalDraft: boolean
  onDownloadDraft: () => void
  onAccepted?: (rev?: number) => void
}) {
  const [view, setView] = useState<View>({ status: "loading" })
  const [detail, setDetail] = useState<Detail>()
  const latest = useRef({ headRev }); latest.current = { headRev }
  const request = useRef(0)
  const refresh = useCallback(() => {
    let live = true
    client.list().then(items => { if (live) setView({ status: "ready", items }) }, () => { if (live) setView({ status: "error" }) })
    return () => { live = false }
  }, [client])
  useEffect(() => refresh(), [refresh])
  function open(id: string) {
    const mine = ++request.current
    setDetail({ status: "loading" })
    // Only the newest click may fill the panel; an older, slower answer is dropped.
    client.read(id).then(value => { if (request.current === mine) setDetail({ status: "ready", value, reviewedRev: latest.current.headRev }) }, () => { if (request.current === mine) setDetail({ status: "error" }) })
  }
  return <section aria-label="Document proposals" className="space-y-3">
    <h2 className="font-semibold">Proposals</h2>
    {view.status === "loading" && <p role="status">Loading proposals…</p>}
    {view.status === "error" && <p role="alert">Proposals could not be loaded.</p>}
    {view.status === "ready" && (view.items.length === 0 ? <p>No proposals.</p> : <ul className="space-y-2">{view.items.map(item => <li key={item.id}>
      <button type="button" className="min-h-11 w-full rounded-md border p-2 text-left" onClick={() => open(item.id)}>{item.author} · {item.status} · revision {item.baseRev}</button>
    </li>)}</ul>)}
    {detail?.status === "loading" && <p role="status">Loading proposal…</p>}
    {detail?.status === "error" && <p role="alert">This proposal could not be loaded.</p>}
    {detail?.status === "ready" && <ProposalReview key={detail.value.id} proposal={detail.value} headRev={headRev} reviewedRev={detail.reviewedRev} canDecide={canDecide}
      hasLocalDraft={hasLocalDraft} onDownloadDraft={onDownloadDraft}
      onAccept={async requestId => { const result = await client.accept(detail.value.id, detail.reviewedRev, requestId); if (result.status === "done") { refresh(); onAccepted?.(result.rev) } return result }}
      onReject={async requestId => { const result = await client.reject(detail.value.id, requestId); if (result.status === "done") refresh(); return result }} />}
  </section>
}
