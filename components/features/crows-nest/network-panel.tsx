"use client"

import { useMemo } from "react"
import { ExternalLink, Network, Plug } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { formatRelativeTime } from "@/lib/time"
import type { JournalEntry } from "@/lib/types/journal"

interface NetworkPanelProps {
  entries: JournalEntry[]
}

interface OpenPort {
  key: string
  port: number | string
  protocol: string
  agentId: string
  openedAt: string
}

interface EgressItem {
  id: string
  host: string
  method: string
  status: number | null
  ts: string
}

/**
 * Two-section panel: currently-open container ports (derived from
 * `network.port_opened` / `network.port_closed` pairs) and the last 10 min of
 * outbound egress (`network.egress`). Both lists are computed from the same
 * entries array the parent already holds.
 */
export function NetworkPanel({ entries }: NetworkPanelProps) {
  const openPorts = useMemo<OpenPort[]>(() => {
    // Walk newest→oldest. When we see a `port_opened`, emit a port unless a
    // later `port_closed` with the same port has already been seen.
    const closed = new Set<string>()
    const open = new Map<string, OpenPort>()
    for (const e of entries) {
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
    return Array.from(open.values())
  }, [entries])

  const recentEgress = useMemo<EgressItem[]>(() => {
    const cutoff = Date.now() - 10 * 60 * 1000
    return entries
      .filter((e) => e.entry_type === "network.egress")
      .filter((e) => new Date(e.ts).getTime() >= cutoff)
      .slice(0, 10)
      .map((e) => ({
        id: e.id,
        host: String(e.payload?.host ?? e.payload?.url ?? ""),
        method: String(e.payload?.method ?? ""),
        status: typeof e.payload?.status === "number" ? (e.payload.status as number) : null,
        ts: e.ts,
      }))
  }, [entries])

  return (
    <div className="flex flex-col h-full bg-card border border-border/50 rounded-lg overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 bg-muted/40 border-b border-border/50 shrink-0">
        <div className="flex items-center gap-2">
          <Network className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-[11px] text-muted-foreground font-medium">Network</span>
        </div>
      </div>

      <div className="flex-1 min-h-0 overflow-auto divide-y divide-border/40">
        <section className="p-3">
          <div className="flex items-center gap-2 mb-2">
            <Plug className="h-3 w-3 text-muted-foreground/80" />
            <h3 className="text-[10px] uppercase tracking-wider text-muted-foreground font-semibold">
              Open ports
            </h3>
            <Badge variant="outline" className="text-[10px] border-border/60 ml-auto">
              {openPorts.length}
            </Badge>
          </div>
          {openPorts.length === 0 ? (
            <div className="text-[11px] text-muted-foreground/60 italic py-2">No open ports observed.</div>
          ) : (
            <ul className="space-y-1">
              {openPorts.map((p) => (
                <li key={p.key} className="flex items-center gap-2 text-[11px]">
                  <code className="font-mono text-foreground/90 bg-muted/40 border border-border/50 rounded px-1.5 py-0.5">
                    {p.port}/{p.protocol}
                  </code>
                  <span className="text-muted-foreground truncate">
                    {p.agentId ? `@${p.agentId.slice(0, 8)}` : "system"}
                  </span>
                  <span className="ml-auto text-[10px] text-muted-foreground font-mono tabular-nums shrink-0">
                    {formatRelativeTime(p.openedAt)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="p-3">
          <div className="flex items-center gap-2 mb-2">
            <ExternalLink className="h-3 w-3 text-muted-foreground/80" />
            <h3 className="text-[10px] uppercase tracking-wider text-muted-foreground font-semibold">
              Recent egress (10m)
            </h3>
            <Badge variant="outline" className="text-[10px] border-border/60 ml-auto">
              {recentEgress.length}
            </Badge>
          </div>
          {recentEgress.length === 0 ? (
            <div className="text-[11px] text-muted-foreground/60 italic py-2">No outbound calls in the last 10 minutes.</div>
          ) : (
            <ul className="space-y-1">
              {recentEgress.map((e) => (
                <li key={e.id} className="flex items-center gap-2 text-[11px]">
                  {e.method && (
                    <Badge variant="outline" className="text-[10px] font-mono border-border/60">
                      {e.method}
                    </Badge>
                  )}
                  <span className="flex-1 min-w-0 truncate text-foreground/80">{e.host || "—"}</span>
                  {e.status !== null && (
                    <Badge
                      variant="outline"
                      className={cn(
                        "text-[10px] font-mono border",
                        e.status >= 400
                          ? "bg-red-500/10 text-red-300 border-red-500/30"
                          : e.status >= 300
                            ? "bg-amber-500/10 text-amber-300 border-amber-500/30"
                            : "bg-emerald-500/10 text-emerald-300 border-emerald-500/30",
                      )}
                    >
                      {e.status}
                    </Badge>
                  )}
                  <span className="text-[10px] text-muted-foreground font-mono tabular-nums shrink-0">
                    {formatRelativeTime(e.ts)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  )
}
