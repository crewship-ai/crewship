"use client"

import { useEffect, useState } from "react"
import { E2CommandSchemas, type ProposalRef } from "@/lib/chat-e2-cards"

export type ProposalScope = { workspaceId: string; viewerId: string; proposalId: string; surface: "card" | "panel" }
export const proposalRecoveryKey = (scope: ProposalScope, action: string) => `chat-proposal:${JSON.stringify([scope.workspaceId, scope.viewerId, scope.proposalId, scope.surface, action])}`
export type ProposalFields = Pick<ProposalRef["proposal"], "title" | "items">
export type ProposalDraft = ProposalFields & { version: number }
export const proposalDraft = (reference: ProposalRef): ProposalDraft => ({ title: reference.proposal.title, items: reference.proposal.items, version: reference.proposal.version })
export function purgeProposalRecovery(scope: ProposalScope) {
  for (const surface of ["card", "panel"] as const) for (const action of ["fields", "comment", "edit", "confirm", "discard"]) {
    try { sessionStorage.removeItem(proposalRecoveryKey({ ...scope, surface }, action)) } catch { /* Best effort on explicit access loss. */ }
  }
}

/** Callers mount with a workspace/viewer/source/surface key, never a version key. */
export function useProposalDraft(reference: ProposalRef, scope: ProposalScope) {
  const fieldKey = proposalRecoveryKey(scope, "fields")
  const commentKey = proposalRecoveryKey(scope, "comment")
  const [draft, setDraft] = useState<ProposalDraft>(() => {
    try {
      const value = JSON.parse(sessionStorage.getItem(fieldKey) ?? "null")
      if (value && E2CommandSchemas.proposal_edit.safeParse({ expected_version: value.version, title: value.title, items: value.items }).success) return value
    } catch { /* A missing draft is safe; writes separately require durable recovery. */ }
    return proposalDraft(reference)
  })
  const [comment, setComment] = useState(() => { try { return sessionStorage.getItem(commentKey) ?? "" } catch { return "" } })
  const [storageError, setStorageError] = useState(false)
  useEffect(() => {
    try { sessionStorage.setItem(fieldKey, JSON.stringify(draft)); setStorageError(false) } catch { setStorageError(true) }
  }, [draft, fieldKey])
  useEffect(() => { try { sessionStorage.setItem(commentKey, comment) } catch { setStorageError(true) } }, [comment, commentKey])
  const conflict = reference.proposal.status === "draft" && draft.version !== reference.proposal.version
  // Even an apparently clean draft waits for an explicit review when the source
  // changes. This prevents a refresh from silently moving the reviewed version.
  return { draft, setDraft, comment, setComment, conflict, storageError,
    useCurrent: () => setDraft(proposalDraft(reference)) }
}
