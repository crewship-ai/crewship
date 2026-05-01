"use client"

import { useMemo } from "react"
import { Bell, Wrench } from "lucide-react"
import type { Mission, MissionTask } from "@/lib/types/mission"

interface MissionWatchRosterProps {
  mission: Mission
}

/**
 * MissionWatchRoster is the right-rail panel from the wireframe:
 *   1. Agent Inbox — items waiting on the operator (approvals, questions,
 *      results to acknowledge). Derived from the mission's tasks: every
 *      AWAITING_APPROVAL task is an approval prompt; tasks with
 *      result_summary that haven't been ack'd surface as "result" rows.
 *   2. Capability Map — agents that own at least one task in this mission
 *      and the tools each one has access to. Tools come from
 *      task.assigned_agent_id-related agent metadata if available;
 *      otherwise an "@agent" row with no tool chips serves as a
 *      placeholder until the agent metadata fan-in lands.
 *
 * Both panels are fed from the Mission record we already have — no
 * extra fetches — so the rail renders synchronously alongside the
 * three modes. Real-time refresh comes from the parent page's SSE
 * subscription which re-supplies a fresh Mission on task.updated.
 */
export function MissionWatchRoster({ mission }: MissionWatchRosterProps) {
  // Stable reference for the deps array — `mission.tasks ?? []` would
  // produce a fresh array on every render and churn both useMemos.
  const tasks = useMemo(() => mission.tasks ?? [], [mission.tasks])
  const inboxItems = useMemo(() => deriveInboxItems(tasks), [tasks])
  const agentTools = useMemo(() => deriveAgentTools(tasks), [tasks])

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-lg border bg-card p-4">
        <div className="flex items-center gap-2 text-sm font-semibold mb-3">
          <Bell className="h-4 w-4" />
          Agent Inbox
          {inboxItems.length > 0 && (
            <span className="ml-auto inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-amber-500 px-1.5 text-[11px] font-bold text-white">
              {inboxItems.length}
            </span>
          )}
        </div>
        {inboxItems.length === 0 ? (
          <p className="text-xs text-muted-foreground italic">
            Nothing waiting on you. ✓
          </p>
        ) : (
          <ul className="flex flex-col gap-3">
            {inboxItems.map((item) => (
              <InboxRow key={item.id} item={item} />
            ))}
          </ul>
        )}
      </div>

      <div className="rounded-lg border bg-card p-4">
        <div className="flex items-center gap-2 text-sm font-semibold mb-3">
          <Wrench className="h-4 w-4" />
          Capability Map
        </div>
        {agentTools.length === 0 ? (
          <p className="text-xs text-muted-foreground italic">
            No agents assigned to this mission yet.
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {agentTools.map(({ agent, tools }) => (
              <li key={agent} className="flex items-start gap-3 border-b last:border-b-0 pb-2 last:pb-0">
                <span className="font-mono text-xs font-semibold text-blue-600 dark:text-blue-400 min-w-[100px]">
                  @{agent}
                </span>
                <span className="flex flex-wrap gap-1 flex-1">
                  {tools.length === 0 ? (
                    <span className="text-[10px] text-muted-foreground italic">
                      tool list not loaded
                    </span>
                  ) : (
                    tools.map((tool) => (
                      <span
                        key={tool}
                        className="px-1.5 py-0.5 bg-muted rounded text-[10px] text-foreground/80"
                      >
                        {tool}
                      </span>
                    ))
                  )}
                </span>
              </li>
            ))}
          </ul>
        )}
        <p className="text-[10px] text-muted-foreground/80 italic mt-3 leading-relaxed">
          Each agent only sees its own tools — strict capability isolation.
        </p>
      </div>
    </div>
  )
}

interface InboxItem {
  id: string
  kind: "approval" | "question" | "result"
  agent: string
  body: string
}

function deriveInboxItems(tasks: MissionTask[]): InboxItem[] {
  const out: InboxItem[] = []
  for (const t of tasks) {
    if (t.status === "AWAITING_APPROVAL") {
      out.push({
        id: t.id,
        kind: "approval",
        agent: t.agent_slug ?? t.agent_name ?? "unknown",
        body: t.result_summary ?? `Approve task: ${t.title}`,
      })
      continue
    }
    // Surface results that explicitly request a human glance —
    // needs_review is the dedicated "operator should look at this"
    // signal. Gating on approved_at would keep every completed task
    // with a result_summary in the inbox forever, since approved_at
    // stays null on tasks that never required approval.
    if (
      (t.status === "COMPLETED" || t.status === "SKIPPED") &&
      t.needs_review &&
      t.result_summary
    ) {
      out.push({
        id: t.id,
        kind: "result",
        agent: t.agent_slug ?? t.agent_name ?? "unknown",
        body: t.result_summary,
      })
    }
  }
  return out
}

function InboxRow({ item }: { item: InboxItem }) {
  const tone = item.kind === "approval"
    ? "border-amber-500/60 bg-amber-500/5"
    : item.kind === "question"
    ? "border-blue-500/60 bg-blue-500/5"
    : "border-emerald-500/60 bg-emerald-500/5"
  const icon = item.kind === "approval" ? "⚠️" : item.kind === "question" ? "💬" : "✅"
  const label = item.kind === "approval" ? "Approval" : item.kind === "question" ? "Question" : "Result"
  return (
    <li className={`rounded border-l-2 ${tone} p-3`}>
      <div className="text-xs font-semibold mb-1 flex items-center gap-1">
        <span>{icon}</span>
        <span>{label}</span>
        <span className="ml-auto font-mono text-[10px] text-blue-600 dark:text-blue-400">
          @{item.agent}
        </span>
      </div>
      <div className="text-xs text-foreground/85 line-clamp-3">{item.body}</div>
    </li>
  )
}

function deriveAgentTools(tasks: MissionTask[]): { agent: string; tools: string[] }[] {
  const seen = new Map<string, Set<string>>()
  for (const t of tasks) {
    const slug = t.agent_slug ?? t.agent_name
    if (!slug) continue
    if (!seen.has(slug)) seen.set(slug, new Set())
    // The MissionTask shape doesn't carry a tool list today. Once it
    // does (or once the page fetches /api/v1/agents/:id and threads
    // tools through), this set will populate; until then the empty
    // set surfaces as "tool list not loaded" in the UI.
  }
  return Array.from(seen.entries())
    .map(([agent, tools]) => ({ agent, tools: Array.from(tools).sort() }))
    .sort((a, b) => a.agent.localeCompare(b.agent))
}
