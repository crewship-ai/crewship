"use client"

import { useCallback, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type { Mission } from "@/lib/types/mission"

/** What the issue page can be asked to run beyond the work endpoint. */
export type IssueWorkflowAction = "start" | "stop" | "stop_now" | "approve" | "request_changes" | "reopen" | "reopen_todo"

export type WorkDialog =
  | { kind: "handoff"; to?: "agent" | "person"; retry?: boolean }
  | { kind: "delegate" }
  | { kind: "submit" }
  | { kind: "request_changes" }
  | null

export interface IssueWork {
  busy: boolean
  /** What the server said the last time it refused, until the next try. */
  error: string | null
  dialog: WorkDialog
  openDialog: (d: Exclude<WorkDialog, null>) => void
  closeDialog: () => void
  takeOver: () => Promise<boolean>
  /** Hand the work to an agent or a person; `start` starts the agent's run after. */
  handOff: (to: "agent" | "person", targetId: string, note: string, opts?: { start?: boolean }) => Promise<boolean>
  submit: (note: string) => Promise<boolean>
  setClientAcceptance: (required: boolean) => Promise<boolean>
  retryStop: () => Promise<boolean>
  run: (action: IssueWorkflowAction, comment?: string) => Promise<boolean>
}

/**
 * The work and review actions of one issue, shared by the session block and
 * the Work & control card so both offer the same verbs with the same rules.
 *
 * Work transfers carry the revision the reader saw and an operation id that
 * stays the same for a retry of the same request, so a double submit is one
 * transfer and a stale page gets a 409 instead of overwriting someone.
 */
export function useIssueWork({
  issue,
  onChanged,
  runWorkflow,
}: {
  issue: Mission
  onChanged: () => Promise<void>
  /** start / stop / review / reopen — owned by the detail, which also toasts. */
  runWorkflow: (action: IssueWorkflowAction, comment?: string) => Promise<boolean>
}): IssueWork {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dialog, setDialog] = useState<WorkDialog>(null)
  const operation = useRef<{ body: string; id: string } | null>(null)
  const base = `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier ?? issue.id)}`
  const qs = `workspace_id=${encodeURIComponent(issue.workspace_id)}`

  const guarded = useCallback(async (fn: () => Promise<void>): Promise<boolean> => {
    setBusy(true)
    setError(null)
    try {
      await fn()
      return true
    } catch (err) {
      setError(err instanceof Error ? err.message : "The server refused the change")
      return false
    } finally {
      setBusy(false)
    }
  }, [])

  const work = useCallback(
    (action: string, targetId = "", note = "") =>
      guarded(async () => {
        const body = JSON.stringify({ action, target_id: targetId, note, revision: issue.work_revision ?? 0 })
        if (operation.current?.body !== body) operation.current = { body, id: crypto.randomUUID() }
        const res = await apiFetch(`${base}/work?${qs}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ ...JSON.parse(body), operation_id: operation.current.id }),
        })
        if (!res.ok) {
          const problem = await res.json().catch(() => null)
          // A 409 means the page is behind: refresh it, keep what was typed.
          if (res.status === 409) await onChanged()
          throw new Error(problem?.detail || problem?.error || "Could not transfer work")
        }
        operation.current = null
        await onChanged()
      }),
    [guarded, base, qs, issue.work_revision, onChanged],
  )

  const takeOver = useCallback(() => work("take_over"), [work])

  const handOff = useCallback(
    async (to: "agent" | "person", targetId: string, note: string, opts?: { start?: boolean }) => {
      const ok = await work(to === "agent" ? "handoff_agent" : "handoff_human", targetId, note)
      if (!ok) return false
      setDialog(null)
      // Handing off prepares the issue; it does not start a run on its own.
      if (to === "agent" && opts?.start) await runWorkflow("start")
      return true
    },
    [work, runWorkflow],
  )

  const submit = useCallback(
    async (note: string) => {
      const ok = await work("submit", "", note)
      if (ok) setDialog(null)
      return ok
    },
    [work],
  )

  const setClientAcceptance = useCallback(
    (required: boolean) =>
      guarded(async () => {
        const res = await apiFetch(`${base}/review-policy?${qs}`, {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ revision: issue.work_revision, client_review_required: required }),
        })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          throw new Error(b?.detail || "Could not update acceptance")
        }
        await onChanged()
      }),
    [guarded, base, qs, issue.work_revision, onChanged],
  )

  const retryStop = useCallback(
    () =>
      guarded(async () => {
        const res = await apiFetch(`${base}/stop?hard=true&${qs}`, { method: "POST" })
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          throw new Error(b?.detail || b?.error || "Could not stop agents")
        }
        await onChanged()
      }),
    [guarded, base, qs, onChanged],
  )

  const run = useCallback(
    async (action: IssueWorkflowAction, comment?: string) => {
      setBusy(true)
      setError(null)
      try {
        const ok = await runWorkflow(action, comment)
        if (ok && action === "request_changes") setDialog(null)
        return ok
      } finally {
        setBusy(false)
      }
    },
    [runWorkflow],
  )

  const openDialog = useCallback((d: Exclude<WorkDialog, null>) => {
    setError(null)
    setDialog(d)
  }, [])
  const closeDialog = useCallback(() => setDialog(null), [])

  return { busy, error, dialog, openDialog, closeDialog, takeOver, handOff, submit, setClientAcceptance, retryStop, run }
}
