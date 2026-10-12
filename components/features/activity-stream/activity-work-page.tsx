"use client"

// Agent work started outside any routine, opened from the rail (#2989).
//
// A delegation from a chat, an issue mention, lead planning: the index lists
// these as chains rooted in an assignment, and a row opens THIS — the same
// shape as a run's page, so the two read alike: what the work was asked to
// do, how it stands in the rail's words, what started it, what it delegated
// and asked, and what it is linked to. Everything comes from stored links
// (docs/specs/activity-links.md); nothing is grouped by time or by agent.

import * as React from "react"
import { Bot, GitBranch, Link2 } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Appear, StatStrip } from "@/components/ui/detail"
import type { ChainSummary } from "@/hooks/use-chains"
import { chainStatus, startedByWord } from "@/lib/activity-lenses"
import { RUN_TONE_CLASS, RUN_TONE_DOT, RUN_TONE_LABEL, chainBranches, linkedEntities } from "@/lib/activity-run"
import { formatDurationMs } from "@/lib/activity-stream"
import { relTime } from "@/lib/time"
import { cn } from "@/lib/utils"

import { BranchRow, LinkChip, countTree, useChainGraph } from "./activity-run-pieces"

export interface ActivityWorkPageProps {
  workspaceId: string
  chain: ChainSummary
  onOpenNode: (kind: string, ref: string) => void
}

export function ActivityWorkPage({ workspaceId, chain, onOpenNode }: ActivityWorkPageProps) {
  const graph = useChainGraph(workspaceId, chain.origin)
  const tone = chainStatus(chain)
  const branches = React.useMemo(
    () => (graph ? chainBranches(graph, chain.origin, "assignment") : []),
    [graph, chain.origin],
  )
  const links = linkedEntities({
    issues: chain.issues,
    graphIssues: graph?.nodes.filter((n) => n.kind === "issue").map((n) => ({ ref: n.ref, label: n.label })),
    agents: chain.agents,
  })
  const trigger = startedByWord(chain)
  const title = chain.task?.trim() || "Agent work"

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      <Appear order={0}>
        <header className="flex flex-col gap-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="inline-flex items-center gap-1 rounded border border-foreground/[0.08] px-1.5 py-px font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
              <Bot className="h-3 w-3" /> Agent work
            </span>
            <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", RUN_TONE_CLASS[tone])}>
              <span className="relative inline-flex h-2 w-2">
                {tone === "running" && (
                  <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", RUN_TONE_DOT[tone])} />
                )}
                <span className={cn("relative h-2 w-2 rounded-full", RUN_TONE_DOT[tone])} />
              </span>
              {RUN_TONE_LABEL[tone]}
            </span>
          </div>
          <h1 className="min-w-0 text-lg font-semibold tracking-tight">{title}</h1>
          <p className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
            {trigger && (
              <>
                <span>{trigger}</span>
                <span aria-hidden>·</span>
              </>
            )}
            <span title={new Date(chain.first_activity).toLocaleString()}>{relTime(chain.first_activity)}</span>
            <span aria-hidden>·</span>
            <span className="font-mono text-muted-foreground-soft">{chain.origin}</span>
          </p>
        </header>
      </Appear>

      <Appear order={1}>
        <StatStrip
          items={[
            {
              label: "Started",
              value: new Date(chain.first_activity).toLocaleString(undefined, {
                hour12: false,
                dateStyle: "short",
                timeStyle: "short",
              }),
            },
            {
              label: "Duration",
              value: chain.duration_ms != null ? formatDurationMs(chain.duration_ms) : "—",
              mono: true,
            },
            { label: "Pieces of work", value: String(chain.runs) },
            {
              label: "Could not finish",
              value: String(chain.failed_runs),
              tone: chain.failed_runs > 0 ? "destructive" : "default",
            },
            { label: "Agents", value: String(chain.agent_count) },
          ]}
        />
      </Appear>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
        <Appear order={2}>
          <DashboardCard
            role="region"
            aria-label="Delegations and asks"
            title="Delegations and asks"
            icon={GitBranch}
            hint={branches.length > 0 ? `${countTree(branches)}` : undefined}
          >
            {branches.length === 0 ? (
              <p className="text-xs text-muted-foreground">
                {graph ? "This work delegated nothing and asked nobody." : "Walking the work…"}
              </p>
            ) : (
              <ul className="flex flex-col">
                {branches.map((n) => (
                  <BranchRow key={n.id} node={n} depth={0} onOpenNode={onOpenNode} />
                ))}
              </ul>
            )}
          </DashboardCard>
        </Appear>

        <Appear order={3}>
          <DashboardCard role="region" aria-label="Linked to" title="Linked to" icon={Link2}>
            {links.length === 0 ? (
              <p className="text-xs text-muted-foreground">No issue or agent is linked to this work.</p>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {links.map((l) => (
                  <LinkChip key={`${l.kind}:${l.ref}`} link={l} onOpenNode={onOpenNode} />
                ))}
              </div>
            )}
          </DashboardCard>
        </Appear>
      </div>
    </div>
  )
}
