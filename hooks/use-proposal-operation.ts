"use client"

import { useEffect, useRef, useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { E2VersionConflictSchema, ProposalRefSchema, type ProposalRef } from "@/lib/chat-e2-cards"
import { randomUUIDv4 } from "@/lib/random-id"
import { proposalRecoveryKey, purgeProposalRecovery, type ProposalScope } from "./use-proposal-draft"

export type ProposalAction = "edit" | "confirm" | "discard"
export type ProposalRequest = { action: ProposalAction; body: string; key: string }
export type ProposalTransport = (request: ProposalRequest) => Promise<Response>
export type ProposalWriteState = "idle" | "saving" | "unknown" | "conflict" | "validation" | "unavailable"

/** Transport uses the same durable request body and key until its outcome is known. */
export function useProposalOperation(scope: ProposalScope, transport: ProposalTransport, onSnapshot: (snapshot: ProposalRef, action?: ProposalAction) => void, onAccessLost: () => void) {
  const [restored] = useState<ProposalRequest | null>(() => {
    for (const action of ["edit", "confirm", "discard"] as const) {
      try {
        const request = JSON.parse(sessionStorage.getItem(proposalRecoveryKey(scope, action)) ?? "null")
        if (request?.action === action && typeof request.body === "string" && typeof request.key === "string") return request
      } catch { /* submit will fail closed if recovery storage is unavailable */ }
    }
    return null
  })
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const pending = useRef<ProposalRequest | null>(restored)
  const busy = useRef(false)
  const [state, setState] = useState<ProposalWriteState>(restored ? "unknown" : "idle")
  const [message, setMessage] = useState("")
  const [current, setCurrent] = useState<ProposalRef | null>(null)
  const mutation = useMutation({ mutationFn: transport, retry: false })
  const clear = (request: ProposalRequest) => { sessionStorage.removeItem(proposalRecoveryKey(scope, request.action)); pending.current = null }
  const execute = async (request: ProposalRequest) => {
    if (!mounted.current || busy.current) return false
    busy.current = true
    setState("saving"); setMessage("")
    try {
      const response = await mutation.mutateAsync(request)
      if ([401, 403, 404].includes(response.status)) {
        purgeProposalRecovery(scope); pending.current = null
        if (mounted.current) { setState("unavailable"); onAccessLost() }
        return false
      }
      if (!mounted.current) return false
      const body = await response.json()
      if (!mounted.current) return false
      if (response.ok) {
        const snapshot = ProposalRefSchema.parse(body)
        if (snapshot.entity.id !== scope.proposalId || snapshot.entity.workspace_id !== scope.workspaceId) throw new Error("Wrong source")
        clear(request); setState("idle"); onSnapshot(snapshot, request.action); return true
      }
      if (response.status === 409) {
        const parsed = E2VersionConflictSchema.safeParse(body)
        if (parsed.success && parsed.data.current.kind === "proposal" && parsed.data.current.entity.id === scope.proposalId && parsed.data.current.entity.workspace_id === scope.workspaceId) setCurrent(parsed.data.current)
        setState("conflict"); setMessage("The proposal changed or this operation key conflicts. Review current values before continuing."); return false
      }
      if ([400, 422].includes(response.status)) {
        clear(request); setState("validation"); setMessage("Check the proposal fields and try again."); return false
      }
      throw new Error("Unknown outcome")
    } catch {
      if (mounted.current) { setState("unknown"); setMessage("The outcome is unknown. Retry the identical operation.") }
      return false
    }
    finally { busy.current = false }
  }
  const submit = async (action: ProposalAction, body: object) => {
    if (pending.current || busy.current) return false
    const request = { action, body: JSON.stringify(body), key: randomUUIDv4() }
    try {
      // Another mount must never replace an unresolved intent for this surface.
      for (const candidate of ["edit", "confirm", "discard"]) if (sessionStorage.getItem(proposalRecoveryKey(scope, candidate))) { setState("unknown"); return false }
      sessionStorage.setItem(proposalRecoveryKey(scope, action), JSON.stringify(request))
    } catch { setState("validation"); setMessage("Recovery storage is unavailable. No request was sent."); return false }
    pending.current = request
    return execute(request)
  }
  return { state, message, current, submit, retry: () => pending.current ? execute(pending.current) : Promise.resolve(false),
    reviewCurrent: (snapshot?: ProposalRef) => {
      const reviewed = snapshot ?? current
      if (!reviewed || busy.current || reviewed.entity.id !== scope.proposalId || reviewed.entity.workspace_id !== scope.workspaceId) return
      if (pending.current) clear(pending.current)
      onSnapshot(reviewed); setCurrent(null); setState("idle")
    } }
}
