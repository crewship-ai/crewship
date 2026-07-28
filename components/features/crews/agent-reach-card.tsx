"use client"

import Link from "next/link"
import { ArrowUpRight, Bell, Wrench } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { useAgentReach } from "@/hooks/use-agent-reach"
import { ProviderMark } from "@/components/features/integrations/provider-marks"
import { brandLogo } from "@/components/features/integrations/composio/shared"

/**
 * What this agent can reach outside itself.
 *
 * Both halves of the answer existed only on the Integrations page, and only
 * from the other end: you could open Gmail and see Riley, or open a channel
 * and see who may post to it. Standing in front of Riley, there was nowhere
 * that said "Gmail, full access" — which is the direction a reviewer actually
 * reads it in, and the direction that matters for "is this agent
 * over-privileged?".
 *
 * Read-only by design. Granting an app belongs to Agent access and granting a
 * channel belongs to the channel, because in both cases the authority sits
 * with whoever owns the destination — not with whoever is looking at the
 * agent. Each half links to the place that owns it.
 */

const MODE_HINT: Record<string, string> = {
  full: "Every tool on this app",
  read: "Read-only tools (fetch, list, get, search)",
  custom: "A hand-picked set of tools",
}

const MODE_STYLE: Record<string, string> = {
  full: "border-emerald-400/25 bg-emerald-400/10 text-emerald-300",
  read: "border-sky-400/25 bg-sky-400/10 text-sky-300",
  custom: "border-amber-400/25 bg-amber-400/10 text-amber-300",
}

export function AgentReachCard({
  workspaceId,
  agentId,
  agentName,
}: {
  workspaceId: string
  agentId: string
  agentName: string
}) {
  const { toolkits, channels, loading } = useAgentReach(workspaceId, agentId)

  return (
    <section className="space-y-3">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold">Reach</h2>
        <Link
          href="/integrations"
          className="inline-flex items-center gap-1 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
        >
          Integrations
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        <Panel
          icon={Wrench}
          title="Apps it can act through"
          count={loading ? undefined : toolkits.length}
        >
          {loading ? (
            <Rows />
          ) : toolkits.length === 0 ? (
            <Empty>
              No connected apps. {agentName} can only use its own tools — grant one on{" "}
              <Link href="/integrations" className="text-primary-hover hover:underline">
                Agent access
              </Link>
              .
            </Empty>
          ) : (
            <div className="flex flex-wrap gap-1.5 px-4 py-3">
              {toolkits.map((t) => (
                <span
                  key={`${t.toolkit}:${t.user_id}`}
                  title={MODE_HINT[t.mode] ?? t.mode}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px]",
                    MODE_STYLE[t.mode] ?? "border-white/10 bg-white/[0.03] text-muted-foreground",
                  )}
                >
                  <ProviderMark
                    provider={t.toolkit}
                    label={t.toolkit}
                    logoUrl={brandLogo(t.toolkit)}
                    className="h-4 w-4 rounded-[4px]"
                  />
                  <span className="capitalize">{t.toolkit}</span>
                  <span className="font-mono text-[10px] opacity-80">{t.mode}</span>
                </span>
              ))}
            </div>
          )}
        </Panel>

        <Panel
          icon={Bell}
          title="Channels it may post to"
          count={loading ? undefined : channels.length}
        >
          {loading ? (
            <Rows />
          ) : channels.length === 0 ? (
            <Empty>
              None. {agentName} cannot send a notification of its own accord — that is the default
              until someone pairs it with a channel.
            </Empty>
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {channels.map((c) => (
                <li key={c.id} className="flex items-center gap-2 px-4 py-2 text-xs">
                  <ProviderMark
                    provider={c.provider || c.type}
                    label={c.provider || c.type}
                    className="h-4 w-4 rounded-[4px]"
                  />
                  <span className="min-w-0 flex-1 truncate capitalize text-foreground/85">
                    {c.provider || c.type}
                  </span>
                  {!c.enabled && (
                    <span
                      className="shrink-0 rounded-full border border-white/10 bg-white/[0.03] px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                      title="The pairing stands, but the channel is switched off — nothing is delivered."
                    >
                      disabled
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </section>
  )
}

function Panel({
  icon: Icon,
  title,
  count,
  children,
}: {
  icon: typeof Wrench
  title: string
  count?: number
  children: React.ReactNode
}) {
  return (
    <div className="overflow-hidden rounded-xl border border-white/8 bg-card">
      <div className="flex items-center gap-2 border-b border-white/5 px-4 py-2.5">
        <Icon className="h-3 w-3 shrink-0 text-muted-foreground/70" />
        <h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
          {title}
        </h3>
        {count != null && (
          <span className="font-mono text-[10px] text-muted-foreground/60">{count}</span>
        )}
      </div>
      {children}
    </div>
  )
}

function Empty({ children }: { children: React.ReactNode }) {
  return <p className="px-4 py-3 text-xs leading-relaxed text-muted-foreground">{children}</p>
}

function Rows() {
  return (
    <div className="space-y-2 px-4 py-3">
      <Skeleton className="h-4 w-40 rounded" />
      <Skeleton className="h-4 w-24 rounded" />
    </div>
  )
}
