"use client"

import Link from "next/link"
import { Clock, Plus, RotateCcw } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { adapterLabel } from "@/lib/credentials/provider-logins"
import { getModelLabel } from "@/lib/cli-adapters"
import { effectiveStatus, latestHireReason } from "@/lib/agent-ephemeral"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"

import { CrCard, CrLine, CrNote, LeadMark } from "./cr-card"
import { formatSpend } from "./crew-spend"
import { AUTONOMY_LABEL } from "./crew-header"
import { orderAgents, tempHireLabel } from "./crews-panel-model"
import type { AgentSummary } from "./crew-canvas-tabs/types"

export const TOOL_ACCESS_LABEL: Record<string, { label: string; hint: string }> = {
  MINIMAL: { label: "Read and plan", hint: "Reads files and the web, writes nothing" },
  CODING: { label: "Workspace work", hint: "Edits files in its folder and runs tools" },
  FULL: { label: "Full tool access", hint: "Anything in the container, including the shell" },
}

export type HireState = "pending" | "active" | "expired"

/** Which of the three a temporary hire is in; null for a permanent agent. */
export function hireState(a: Pick<AgentSummary, "ephemeral" | "expired_at" | "status">): HireState | null {
  if (!a.ephemeral) return null
  if (a.expired_at) return "expired"
  if (a.status === "PENDING_REVIEW") return "pending"
  return "active"
}

export interface CrewTeamProps {
  agents: AgentSummary[]
  /** Runs per agent over the last 30 days (GET /runs/insights by_agent). */
  runsByAgent?: ReadonlyMap<string, number> | null
  autonomy: string | null
  /** max_ephemeral_agents of the crew. */
  hireLimit: number | null
  leadName?: string | null
  onSelectAgent: (slug: string) => void
  onNewAgent: () => void
  onHire: () => void
  onApprove: (agent: AgentSummary) => void
  onRehire: (agent: AgentSummary) => void
  /**
   * Give a live hire more time (POST /agents/{id}/rehire on a live agent
   * extends it). Absent for a reader who may not hire: no button.
   */
  onExtend?: (agent: AgentSummary) => void
  /**
   * End a live hire now (POST /agents/{id}/release): it becomes an expired
   * hire, its runs and notes stay and Rehire brings it back. Absent for a
   * reader who may not hire.
   */
  onRelease?: (agent: AgentSummary) => void
  /**
   * Cost over 30 days per agent id (GET /paymaster/spend/by-agent/{crewId}).
   * Null or absent = unknown or not for this reader: the column stays (as in
   * the design) and shows a dash rather than zeros.
   */
  costByAgent?: ReadonlyMap<string, number> | null
  busyAgentId?: string | null
  now?: number
}

/**
 * C-TEAM: the permanent agents as a table, the temporary hires with what
 * each one waits for (approve, time left, rehire) and how the team works.
 */
export function CrewTeam({
  agents,
  runsByAgent = null,
  autonomy,
  hireLimit,
  leadName,
  onSelectAgent,
  onNewAgent,
  onHire,
  onApprove,
  onRehire,
  onExtend,
  onRelease,
  costByAgent = null,
  busyAgentId = null,
  now = Date.now(),
}: CrewTeamProps) {
  const permanent = orderAgents(agents.filter((a) => !a.ephemeral))
  const temporary = orderAgents(agents.filter((a) => a.ephemeral))
  const inUse = temporary.filter((a) => !a.expired_at).length
  const auto = autonomy ? AUTONOMY_LABEL[autonomy]?.label ?? autonomy : null

  return (
    <div className="space-y-4" data-testid="crew-team">
      <CrCard
        title="Agents"
        concept="crews"
        count={permanent.length}
        action={<Button size="sm" onClick={onNewAgent}><Plus className="size-3.5" aria-hidden /> New agent</Button>}
      >
        {permanent.length === 0 ? (
          <CrNote>No agents yet. The first agent you add becomes the lead.</CrNote>
        ) : (
          <div className="overflow-x-auto border-t border-border">
            <table className="w-full text-body">
              <thead>
                <tr className="text-left text-label text-muted-foreground">
                  <th className="px-4 py-2 font-medium">Agent</th>
                  <th className="px-3 py-2 font-medium max-md:hidden">Runs with</th>
                  <th className="px-3 py-2 font-medium max-lg:hidden">Tool access</th>
                  <th className="px-3 py-2 font-medium">Status</th>
                  <th className="px-3 py-2 font-medium max-md:hidden">Skills</th>
                  <th className="px-3 py-2 font-medium max-md:hidden">Credentials</th>
                  <th className="px-3 py-2 text-right font-medium max-sm:hidden">Runs · 30 d</th>
                  <th className="px-4 py-2 text-right font-medium max-md:hidden">Cost · 30 d</th>
                </tr>
              </thead>
              <tbody>
                {permanent.map((a) => {
                  const st = effectiveStatus(a)
                  const creds = a._count?.credentials ?? null
                  return (
                    <tr
                      key={a.id}
                      className="cursor-pointer border-t border-border hover:bg-foreground/[0.03]"
                      onClick={() => onSelectAgent(a.slug)}
                      data-agent-row={a.slug}
                    >
                      <td className="px-4 py-2">
                        <button type="button" className="flex min-w-0 items-center gap-2.5 text-left coarse:min-h-[44px]" onClick={(e) => { e.stopPropagation(); onSelectAgent(a.slug) }}>
                          <AgentAvatar seed={a.avatar_seed || a.name} style={a.avatar_style} avatarUrl={a.avatar_url} className="size-6 shrink-0 rounded-md" />
                          <span className="min-w-0">
                            <b className="flex items-center gap-1.5 truncate font-medium text-foreground">{a.name}{a.agent_role === "LEAD" && <LeadMark />}</b>
                            {a.role_title && <span className="block truncate text-label text-muted-foreground">{a.role_title}</span>}
                          </span>
                        </button>
                      </td>
                      <td className="px-3 py-2 max-md:hidden">
                        <span className="block">{a.cli_adapter ? adapterLabel(a.cli_adapter) : "—"}</span>
                        {a.llm_model && <span className="block text-label text-muted-foreground">{getModelLabel(a.llm_model)}</span>}
                      </td>
                      <td className="px-3 py-2 max-lg:hidden">{a.tool_profile ? TOOL_ACCESS_LABEL[a.tool_profile]?.label ?? a.tool_profile : "—"}</td>
                      <td className="px-3 py-2"><StatusPill className="font-sans" status={st} live={st === "RUNNING"} /></td>
                      <td className="px-3 py-2 tabular-nums max-md:hidden">{a._count?.skills ?? "—"}</td>
                      <td className="px-3 py-2 max-md:hidden">
                        {creds == null ? "—" : creds === 0 ? <span className="text-chip-warn-fg">none</span> : <span className="tabular-nums">{creds}</span>}
                      </td>
                      <td className="px-3 py-2 text-right tabular-nums max-sm:hidden">{runsByAgent ? runsByAgent.get(a.id) ?? 0 : "—"}</td>
                      <td className="px-4 py-2 text-right tabular-nums max-md:hidden">{costByAgent ? formatSpend(costByAgent.get(a.id) ?? 0) : "—"}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </CrCard>

      <CrCard
        title={`Temporary hires · ${inUse}${hireLimit != null ? ` of ${hireLimit}` : ""} in use`}
        concept="crews"
        icon={Clock}
        action={<Button variant="outline" size="sm" onClick={onHire} disabled={autonomy === "strict"}><Clock className="size-3.5" aria-hidden /> Hire a temporary agent</Button>}
        data-testid="crew-temporary-hires"
      >
        {temporary.length === 0 ? (
          <CrNote>
            No temporary agents. A lead can hire one for up to 24 hours; in this crew{auto ? ` (${auto})` : ""}{" "}
            {autonomy === "guided" ? "you approve each hire in the Inbox" : autonomy === "strict" ? "hires are not allowed" : "it happens on its own and you are told"}.
          </CrNote>
        ) : temporary.map((a) => {
          const state = hireState(a)
          const temp = tempHireLabel(a, now)
          const reason = latestHireReason(a.hire_reason)
          const sub = [
            state === "pending" ? "Waiting for approval" : state === "expired" ? "Expired · runs and notes stay" : `Expires in ${temp?.label ?? "—"}`,
            reason ? `“${reason}”` : null,
          ].filter(Boolean).join(" · ")
          return (
            <CrLine
              key={a.id}
              data-hire-state={state ?? undefined}
              lead={<AgentAvatar seed={a.avatar_seed || a.name} style={a.avatar_style} avatarUrl={a.avatar_url} className="size-7 rounded-md" />}
              title={`${a.name}${a.role_title ? ` · ${a.role_title}` : ""}`}
              sub={sub}
              onOpen={() => onSelectAgent(a.slug)}
              right={
                state === "pending" ? (
                  <>
                    <Button asChild variant="ghost" size="sm"><Link href={entityHref({ kind: "inbox", agentSlug: a.slug })}>Review in Inbox</Link></Button>
                    <Button size="sm" disabled={busyAgentId === a.id} onClick={() => onApprove(a)}>Approve</Button>
                  </>
                ) : state === "expired" ? (
                  <Button variant="outline" size="sm" onClick={() => onRehire(a)}><RotateCcw className="size-3.5" aria-hidden /> Rehire</Button>
                ) : onExtend || onRelease ? (
                  <>
                    {onRelease && <Button variant="ghost" size="sm" disabled={busyAgentId === a.id} onClick={() => onRelease(a)}>Release now</Button>}
                    {onExtend && <Button variant="outline" size="sm" onClick={() => onExtend(a)}><Clock className="size-3.5" aria-hidden /> Extend</Button>}
                  </>
                ) : null
              }
            />
          )
        })}
      </CrCard>

      <CrCard title="How this team works" concept="crews">
        <div className="space-y-2 border-t border-border px-4 py-3 text-body text-muted-foreground">
          <p>
            {leadName ? <><b className="font-medium text-foreground">{leadName}</b> is the lead: plans the work, hands it out and is the only one who edits <code className="font-mono text-foreground">CREW.md</code>.</> : "The crew has no lead yet; the first agent you add becomes the lead."}
          </p>
          <p>
            Any agent can pass work on to another. All agents share one container and run as the same user, so they can read each other’s folders. Put agents that must not see each other’s secrets in different crews.
          </p>
        </div>
      </CrCard>
    </div>
  )
}
