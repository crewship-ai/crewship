"use client"

import { useMemo } from "react"
import { motion } from "motion/react"
import { ExternalLink, Network, Plug } from "lucide-react"
import type { JournalEntry } from "@/lib/types/journal"
import { formatRelativeTime } from "@/lib/time"

interface LogsNetworkCardProps {
  entries: JournalEntry[]
}

interface OpenPort {
  key: string
  port: number | string
  protocol: string
  agentId: string
  openedAt: string
}

interface EgressBucket {
  host: string
  count: number
  lastTs: string
}

const MAX_ROWS = 5

/**
 * Compact network observability card for the LogsStatsRail. Shows currently
 * open container ports + a top-N rollup of recent egress hosts. Derived
 * directly from the same `entries` slice the rest of the rail consumes —
 * the parent gates rendering by `admin && crewSelected`.
 *
 * Logic mirrors the now-deprecated NetworkPanel from /crows-nest, but
 * collapsed for narrow rail width: aggregated by host (count + last_seen)
 * instead of per-event listing.
 */
export function LogsNetworkCard({ entries }: LogsNetworkCardProps) {
  const openPorts = useMemo<OpenPort[]>(() => {
    // Walk in ts order so a port_closed observed *before* its
    // corresponding port_opened in the array (entries can arrive
    // unsorted from SSE batching) doesn't incorrectly mask the open.
    const sorted = [...entries].sort(
      (a, b) => new Date(a.ts).getTime() - new Date(b.ts).getTime(),
    )
    const closed = new Set<string>()
    const open = new Map<string, OpenPort>()
    for (const e of sorted) {
      if (e.entry_type !== "network.port_opened" && e.entry_type !== "network.port_closed") continue
      const port = (e.payload?.port as number | string | undefined) ?? ""
      const proto = (e.payload?.protocol as string | undefined) ?? "tcp"
      const key = `${port}/${proto}`
      if (e.entry_type === "network.port_closed") {
        closed.add(key)
        continue
      }
      if (closed.has(key)) continue
      if (!open.has(key)) {
        open.set(key, {
          key,
          port,
          protocol: proto,
          agentId: e.actor_id ?? "",
          openedAt: e.ts,
        })
      }
    }
    return Array.from(open.values()).slice(0, MAX_ROWS)
  }, [entries])

  const topEgress = useMemo<EgressBucket[]>(() => {
    const cutoff = Date.now() - 10 * 60 * 1000
    const byHost = new Map<string, EgressBucket>()
    for (const e of entries) {
      if (e.entry_type !== "network.egress") continue
      const ts = (e as JournalEntry & { _tsMs?: number })._tsMs ?? new Date(e.ts).getTime()
      if (Number.isNaN(ts) || ts < cutoff) continue
      const host = String(e.payload?.host ?? e.payload?.url ?? "").trim()
      if (!host) continue
      const existing = byHost.get(host)
      if (existing) {
        existing.count += 1
        if (e.ts > existing.lastTs) existing.lastTs = e.ts
      } else {
        byHost.set(host, { host, count: 1, lastTs: e.ts })
      }
    }
    return Array.from(byHost.values())
      .sort((a, b) => b.count - a.count)
      .slice(0, MAX_ROWS)
  }, [entries])

  if (openPorts.length === 0 && topEgress.length === 0) {
    return (
      <motion.div
      variants={{
        hidden: { opacity: 0, y: 6 },
        visible: { opacity: 1, y: 0, transition: { duration: 0.22, ease: "easeOut" } },
      }}
      className="rounded-md border border-border/50 bg-card/40 px-3 py-2"
    >
        <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground mb-2">
          <Network className="h-3 w-3" />
          Network
        </div>
        <div className="text-[11px] text-muted-foreground/60 italic">
          No network activity in window.
        </div>
      </motion.div>
    )
  }

  return (
    <motion.div
      variants={{
        hidden: { opacity: 0, y: 6 },
        visible: { opacity: 1, y: 0, transition: { duration: 0.22, ease: "easeOut" } },
      }}
      className="rounded-md border border-border/50 bg-card/40 px-3 py-2"
    >
      <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground mb-2">
        <Network className="h-3 w-3" />
        Network
      </div>

      {openPorts.length > 0 && (
        <div className="mb-2">
          <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground/70 mb-1">
            <Plug className="h-2.5 w-2.5" />
            Open ports
          </div>
          <ul className="space-y-1 text-[11px] font-mono">
            {openPorts.map((p) => (
              <li key={p.key} className="flex items-center gap-2 min-w-0">
                <code className="text-foreground/90 bg-muted/40 border border-border/50 rounded px-1 text-[10px] shrink-0">
                  {p.port}/{p.protocol}
                </code>
                <span className="flex-1 truncate text-muted-foreground" title={p.agentId}>
                  {p.agentId ? `@${p.agentId.slice(0, 8)}` : "system"}
                </span>
                <span className="text-[10px] text-muted-foreground/70 tabular-nums shrink-0">
                  {formatRelativeTime(p.openedAt)}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {topEgress.length > 0 && (
        <div>
          <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground/70 mb-1">
            <ExternalLink className="h-2.5 w-2.5" />
            Egress (10m)
          </div>
          <ul className="space-y-1 text-[11px] font-mono">
            {topEgress.map((e) => (
              <li key={e.host} className="flex items-center gap-2 min-w-0">
                <span className="flex-1 truncate text-foreground/85" title={e.host}>
                  {e.host}
                </span>
                <span className="tabular-nums text-muted-foreground/85 shrink-0">{e.count}×</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </motion.div>
  )
}
