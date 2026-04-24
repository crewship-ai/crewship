"use client"

import { useParams } from "next/navigation"
import { useAgentDetail } from "@/hooks/use-agent-detail"

/**
 * Resolve the active agent id. Route params win when we're on
 * `/crews/agents/[agentId]/*`, otherwise fall back to the
 * `AgentDetailProvider` in scope.
 *
 * This lets panes (files, skills, credentials, mcp) render inside the
 * unified `/crews` canvas — where there is no dynamic `agentId` segment
 * — without a per-consumer prop-drill.
 */
export function useAgentId(): string {
  const params = useParams<{ agentId?: string }>()
  const { agent } = useAgentDetail()
  // Empty string instead of undefined so callers don't need a fallback
  // on every usage. Panes already handle "no data" loading states, and
  // a missing id renders as 404 rather than a crash.
  return params?.agentId ?? agent?.id ?? ""
}
