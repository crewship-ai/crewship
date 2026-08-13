"use client"

// Asks — the questions this agent was configured to offer.
//
// Source of truth is one column: agents.suggested_prompts, newline
// separated, capped server-side at 8 lines of ≤120 runes
// (internal/api/agents_suggested_prompts.go). It rides on both
// GET /api/v1/agents and GET /api/v1/agents/{agentId}; this pane reads the
// detail endpoint because it is addressed by agent id, and parses with the
// shared parseSuggestedPrompts so the cap is applied the same way the chat
// composer applies it.
//
// Read-only, deliberately. lib/agent-suggestions.ts also owns a fallback:
// when an agent has no prompts of its own the composer substitutes a role
// pack ("Plan a refactor of the chat module", …). Those are Crewship's
// defaults, not this agent's configuration, and showing them on a folder
// called Asks would tell an operator their agent was set up when it was
// not. So the fallback stops at the composer; here, unconfigured shows the
// empty state and says where the textarea is.

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { MessageSquareText, Settings2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard, EmptyState } from "@/components/ui/detail"
import { apiFetch } from "@/lib/api-fetch"
import {
  MAX_SUGGESTED_PROMPTS,
  MAX_SUGGESTED_PROMPT_LENGTH,
  parseSuggestedPrompts,
} from "@/lib/agent-suggestions"
import { useWorkspace } from "@/hooks/use-workspace"

import { PaneError, PaneLoading, PaneShell } from "./pane-shell"

export interface AgentAsksPaneProps {
  agentId: string
  agentSlug: string
}

type Status = "loading" | "ready" | "error"

export function AgentAsksPane({ agentId, agentSlug }: AgentAsksPaneProps) {
  const { workspaceId } = useWorkspace()
  const [nonce, setNonce] = useState(0)
  const [status, setStatus] = useState<Status>("loading")
  const [asks, setAsks] = useState<string[]>([])
  const [error, setError] = useState<string>("")

  const retry = useCallback(() => setNonce((n) => n + 1), [])

  useEffect(() => {
    // No workspace resolved yet means the request would 400, not fail —
    // hold the loading state rather than firing a doomed call.
    if (!workspaceId) {
      setStatus("loading")
      return
    }
    const ac = new AbortController()
    setStatus("loading")
    apiFetch(
      `/api/v1/agents/${encodeURIComponent(agentId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
      { signal: ac.signal },
    )
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((agent: { suggested_prompts?: string | null } | null) => {
        if (ac.signal.aborted) return
        setAsks(parseSuggestedPrompts(agent?.suggested_prompts))
        setStatus("ready")
      })
      .catch((e: Error) => {
        if (ac.signal.aborted || e.name === "AbortError") return
        setError(e.message)
        setStatus("error")
      })
    return () => ac.abort()
  }, [agentId, workspaceId, nonce])

  const configHref = `/crews?agent=${encodeURIComponent(agentSlug)}`

  return (
    <PaneShell
      icon={MessageSquareText}
      title="Asks"
      subtitle={
        status === "ready" && asks.length > 0
          ? `${asks.length} prepared question${asks.length === 1 ? "" : "s"} — shown under an empty chat with this agent`
          : "Prepared questions, offered under an empty chat with this agent"
      }
      actions={
        <Button asChild variant="outline" size="sm">
          <Link href={configHref}>
            <Settings2 className="h-3.5 w-3.5" />
            Edit in config
          </Link>
        </Button>
      }
      data-testid="asks-pane"
    >
      {status === "loading" && <PaneLoading label="Loading asks…" data-testid="asks-pane-loading" />}

      {status === "error" && (
        <PaneError
          data-testid="asks-error"
          title="Could not load this agent's asks"
          detail={`GET /api/v1/agents/${agentId} failed — ${error}. The asks live on the agent record, so nothing can be shown until it loads.`}
          onRetry={retry}
        />
      )}

      {status === "ready" && asks.length === 0 && (
        <div data-testid="asks-empty">
          <EmptyState
            icon={MessageSquareText}
            title="No asks configured"
            description={'Set them in the "Chat suggestions" card on this agent’s Config tab. One question per line, up to 8.'}
            action={
              <Button asChild size="sm">
                <Link href={configHref}>Open {agentSlug}’s configuration</Link>
              </Button>
            }
          />
          <p className="type-meta mx-auto mt-2 max-w-sm text-center leading-relaxed text-muted-foreground-soft">
            Until then the chat offers Crewship&rsquo;s defaults for the agent&rsquo;s role. Those are not
            shown here — this folder is what <em>this</em> agent was configured with.
          </p>
        </div>
      )}

      {status === "ready" && asks.length > 0 && (
        <DetailCard
          title="Prepared questions"
          subtitle={`${asks.length}/${MAX_SUGGESTED_PROMPTS}`}
          icon={MessageSquareText}
          bare
          footer={`Read-only here. Edited in the "Chat suggestions" card on the agent's Config tab — up to ${MAX_SUGGESTED_PROMPTS} lines of ${MAX_SUGGESTED_PROMPT_LENGTH} characters.`}
        >
          <ul data-testid="asks-list" className="divide-y divide-hairline">
            {asks.map((ask, i) => (
              <li
                key={`${i}-${ask}`}
                data-testid="ask-row"
                className="flex items-start gap-3 px-4 py-2.5"
              >
                <span className="type-meta w-4 shrink-0 pt-0.5 text-right tabular-nums text-muted-foreground-soft">
                  {i + 1}
                </span>
                <span className="type-row leading-relaxed text-foreground">{ask}</span>
              </li>
            ))}
          </ul>
        </DetailCard>
      )}
    </PaneShell>
  )
}
