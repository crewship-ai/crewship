"use client"

import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"

import { askFormsFromColumn, type AskForm } from "./types"

/**
 * This agent's questionnaire forms.
 *
 * Source is one column, `agents.ask_forms`, riding on both `GET /api/v1/agents`
 * and `GET /api/v1/agents/{agentId}`, as a TEXT column holding a JSON array.
 *
 * `provided` is the normal path, and the chat page takes it: it resolved this
 * agent out of the roster it fetched for the tree, so the column is already in
 * hand and no request is made. An EMPTY array counts as provided — "this agent
 * has no forms" is an answer, and re-asking the server for it was the whole
 * cost this argument exists to remove.
 *
 * The fetch below is the fallback for a caller with no agent record. It reads
 * the DETAIL endpoint because the chat surface is addressed by agent **id**
 * while a roster is addressed by slug.
 *
 * `provided` must be referentially stable across renders (memoise the parse —
 * chat-panel.tsx does) — it is an effect dependency, and a fresh array every
 * render would re-run the effect every render.
 *
 * **Failure is silence, deliberately.** Every error path — no workspace yet, a
 * 404, a 500, malformed JSON, a column holding something unexpected — yields
 * `[]`, and `[]` is precisely today's behaviour: the rail renders the plain
 * suggestion chips and nothing else. An agent with no forms must be
 * indistinguishable from an agent before this feature existed, and a failed
 * fetch of an optional column is not worth an error state in a conversation.
 */
export function useAskForms(agentId: string, provided?: AskForm[] | null): AskForm[] {
  const { workspaceId } = useWorkspace()
  const [forms, setForms] = useState<AskForm[]>(() => provided ?? [])

  useEffect(() => {
    if (provided) {
      setForms(provided)
      return
    }
    if (!agentId || !workspaceId) return

    const ac = new AbortController()
    apiFetch(
      `/api/v1/agents/${encodeURIComponent(agentId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
      { signal: ac.signal },
    )
      .then((r) => (r.ok ? r.json() : null))
      .then((agent: { ask_forms?: string | null } | null) => {
        if (ac.signal.aborted) return
        setForms(askFormsFromColumn(agent?.ask_forms))
      })
      .catch(() => {
        /* see the note above: no forms is the correct answer to a failure */
      })
    return () => ac.abort()
  }, [agentId, workspaceId, provided])

  return forms
}
