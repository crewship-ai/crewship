"use client"

import * as React from "react"
import Link from "next/link"
import { ArrowUpRight, Bell, Bot } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { apiFetch } from "@/lib/api-fetch"
import { readThrough } from "@/lib/stale-cache"
import { cn } from "@/lib/utils"
import { useAgentReach } from "@/hooks/use-agent-reach"
import { ProviderMark } from "@/components/features/integrations/provider-marks"
import { brandLogo } from "@/components/features/integrations/composio/shared"

/**
 * What this routine can reach THROUGH the agents it runs.
 *
 * "What it touches" already lists the routine's own blast radius — its
 * integrations, datastores, tools and the agents it invokes. What it cannot
 * show is the transitive step: the routine runs Riley, Riley is granted Gmail,
 * therefore this routine can send mail. That hop is invisible in the DSL,
 * because the grant lives on the agent, and it is exactly the hop a reviewer
 * needs when asking "what could this actually do if it misbehaved?".
 *
 * Read-only, like the panel above it. Grants belong where they are made.
 */

const MODE_STYLE: Record<string, string> = {
  full: "border-success/25 bg-success/10 text-success",
  read: "border-info/25 bg-info/10 text-info",
  custom: "border-warn/25 bg-warn/10 text-warn",
}

interface AgentLite {
  id: string
  name: string
  slug: string
}

/** Resolve the manifest's agent slugs to ids — the reach lookups are by id. */
function useAgentsBySlug(workspaceId: string | undefined) {
  const [agents, setAgents] = React.useState<AgentLite[] | null>(null)

  React.useEffect(() => {
    if (!workspaceId) return
    let live = true
    const { value, fresh } = readThrough(`agents:${workspaceId}:list`, async () => {
      const r = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!r.ok) throw new Error(String(r.status))
      return (await r.json()) as AgentLite[]
    })
    if (value) setAgents(value)
    fresh.then(
      (v) => live && setAgents(v),
      () => live && setAgents((prev) => prev ?? []),
    )
    return () => {
      live = false
    }
  }, [workspaceId])

  return agents
}

export function RoutineReachCard({
  workspaceId,
  agentSlugs,
}: {
  /** Undefined while the page is still resolving its workspace. */
  workspaceId: string | undefined
  /** Agent slugs the routine invokes, from its manifest. */
  agentSlugs: string[]
}) {
  const agents = useAgentsBySlug(workspaceId)

  // An agentless routine reaches nothing through an agent, and a panel saying
  // so is noise on a page that already says the routine is agentless.
  if (agentSlugs.length === 0 || !workspaceId) return null

  const resolved = agentSlugs.map((slug) => ({
    slug,
    agent: agents?.find((a) => a.slug === slug) ?? null,
  }))

  return (
    <div className="overflow-hidden rounded-xl border border-white/8 bg-card">
      <div className="flex items-center gap-2 border-b border-white/5 px-4 py-2.5">
        <Bot className="h-3 w-3 shrink-0 text-muted-foreground/70" />
        <h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
          Reach through its agents
        </h3>
        <span className="flex-1" />
        <Link
          href="/integrations"
          className="inline-flex items-center gap-1 text-[10px] text-muted-foreground transition-colors hover:text-foreground"
        >
          Integrations
          <ArrowUpRight className="h-2.5 w-2.5" />
        </Link>
      </div>

      {agents === null ? (
        <div className="space-y-2 px-4 py-3">
          <Skeleton className="h-4 w-44 rounded" />
          <Skeleton className="h-4 w-32 rounded" />
        </div>
      ) : (
        <ul className="divide-y divide-white/[0.04]">
          {resolved.map(({ slug, agent }) => (
            <li key={slug} className="px-4 py-2.5">
              {agent ? (
                <AgentRow workspaceId={workspaceId} agent={agent} />
              ) : (
                <div className="flex items-center gap-2 text-xs">
                  <span className="font-mono text-foreground/70">@{slug}</span>
                  <span className="text-[11px] text-muted-foreground">
                    — no such agent in this workspace; the routine would fail here
                  </span>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function AgentRow({ workspaceId, agent }: { workspaceId: string; agent: AgentLite }) {
  const { toolkits, channels, loading } = useAgentReach(workspaceId, agent.id)

  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
      <span className="text-xs font-medium text-foreground/85">{agent.name}</span>
      <span className="font-mono text-[10px] text-muted-foreground/60">@{agent.slug}</span>

      {loading ? (
        <Skeleton className="h-4 w-24 rounded" />
      ) : toolkits.length === 0 && channels.length === 0 ? (
        <span className="text-[11px] text-muted-foreground">— reaches nothing outside Crewship</span>
      ) : (
        <>
          {toolkits.map((t) => (
            <span
              key={`${t.toolkit}:${t.user_id}`}
              title={`${t.toolkit} · ${t.mode}`}
              className={cn(
                "inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-[10px]",
                MODE_STYLE[t.mode] ?? "border-white/10 bg-white/[0.03] text-muted-foreground",
              )}
            >
              <ProviderMark
                provider={t.toolkit}
                label={t.toolkit}
                logoUrl={brandLogo(t.toolkit)}
                className="h-3.5 w-3.5 rounded-[3px]"
              />
              <span className="capitalize">{t.toolkit}</span>
            </span>
          ))}
          {channels.map((c) => (
            <span
              key={c.id}
              title={`May post to a ${c.provider || c.type} channel`}
              className="inline-flex items-center gap-1 rounded-full border border-white/10 bg-white/[0.03] px-1.5 py-0.5 text-[10px] text-muted-foreground"
            >
              <Bell className="h-2.5 w-2.5" />
              <span className="capitalize">{c.provider || c.type}</span>
            </span>
          ))}
        </>
      )}
    </div>
  )
}
