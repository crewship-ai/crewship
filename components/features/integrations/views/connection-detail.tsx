"use client"

import * as React from "react"
import { Bot, ChevronLeft, Send, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"
import { useChannelAgents } from "@/hooks/use-channel-agents"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"
import { NOTIFICATION_CATEGORY_GROUPS } from "@/lib/notification-categories"
import { ProviderMark } from "../provider-marks"
import { STATUS_LABEL, type ConnectionRow } from "../connection-model"

/**
 * One connection, in full.
 *
 * The list answers "what is hooked up"; this answers the questions that
 * follow — who else can post here, what it is allowed to carry, and what it
 * has actually sent. Those lived in three different places before (the row,
 * an admin-only overview, the delivery log), which meant nobody could see a
 * single connection whole.
 */

const STATUS_STYLE: Record<string, string> = {
  delivering: "border-success/30 bg-success/10 text-success",
  failing: "border-destructive/35 bg-destructive/10 text-destructive",
  never_used: "border-warn/30 bg-warn/10 text-warn",
  disabled: "border-white/10 bg-white/[0.03] text-muted-foreground",
  unknown: "border-info/25 bg-info/10 text-info",
}

/** Category key -> the label the preference matrix shows. */
const CATEGORY_LABEL = new Map(
  NOTIFICATION_CATEGORY_GROUPS.flatMap((g) => g.categories.map((c) => [c.key, c.label] as const)),
)

interface ConnectionDetailProps {
  workspaceId: string
  row: ConnectionRow
  /** This connection's slice of the delivery log; null when unreadable. */
  deliveries: NotificationDelivery[] | null
  onBack: () => void
  onToggleEnabled: (row: ConnectionRow, next: boolean) => Promise<void>
  onTest: (row: ConnectionRow) => Promise<void>
  onDelete: (row: ConnectionRow) => Promise<void>
}

export function ConnectionDetail({
  workspaceId,
  row,
  deliveries,
  onBack,
  onToggleEnabled,
  onTest,
  onDelete,
}: ConnectionDetailProps) {
  const { agents, loading: agentsLoading } = useChannelAgents(workspaceId, row.id)
  const [busy, setBusy] = React.useState<"toggle" | "test" | "delete" | null>(null)

  const run = async (kind: "toggle" | "test" | "delete", fn: () => Promise<void>) => {
    setBusy(kind)
    try {
      await fn()
    } finally {
      setBusy(null)
    }
  }

  const recent = (deliveries ?? []).slice(0, 8)

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
        <button
          type="button"
          onClick={onBack}
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          Back to connections
        </button>
        <span className="truncate text-xs font-medium text-foreground/85">{row.name}</span>
      </div>

      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 md:p-6">
        {/* Identity */}
        <div className="flex flex-wrap items-start gap-3 rounded-xl border border-white/[0.08] bg-card px-4 py-3.5">
          <ProviderMark provider={row.provider} label={row.providerLabel} className="h-9 w-9" />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium text-foreground/90">{row.name}</div>
            <div className="truncate font-mono text-[11px] text-muted-foreground">
              {row.providerLabel} · {row.kind} · {row.scope}
            </div>
          </div>
          <div className="flex items-center gap-2">
            <span
              className={cn(
                "inline-flex items-center rounded-full border px-2 py-0.5 text-[10px]",
                STATUS_STYLE[row.status],
              )}
            >
              {STATUS_LABEL[row.status]}
            </span>
            {!row.readOnly && (
              <>
                <Switch
                  size="sm"
                  checked={row.enabled}
                  disabled={busy !== null}
                  onCheckedChange={(next) => run("toggle", () => onToggleEnabled(row, next))}
                  aria-label={row.enabled ? "Disable this connection" : "Enable this connection"}
                />
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 gap-1.5 text-xs"
                  disabled={busy !== null}
                  onClick={() => run("test", () => onTest(row))}
                >
                  {busy === "test" ? <Spinner className="size-3" /> : <Send className="h-3 w-3" />}
                  Send test
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                  disabled={busy !== null}
                  aria-label="Delete this connection"
                  onClick={() => run("delete", () => onDelete(row))}
                >
                  {busy === "delete" ? <Spinner className="size-3" /> : <Trash2 className="size-3" />}
                </Button>
              </>
            )}
          </div>
          {row.readOnly && (
            <p className="w-full text-[11px] text-muted-foreground">
              This is another member&apos;s personal connection. You can see that it exists and
              what kind it is; where it points, and whether it stays, is theirs.
            </p>
          )}
        </div>

        <div className="grid gap-4 lg:grid-cols-2">
          {/* Categories */}
          <Panel title="Categories it may carry">
            {row.categories.length === 0 ? (
              <p className="px-4 py-3 text-xs text-muted-foreground">
                Every category. Nobody has narrowed this connection, so any category a person
                routes here will be delivered.
              </p>
            ) : (
              <div className="flex flex-wrap gap-1.5 px-4 py-3">
                {row.categories.map((c) => (
                  <span
                    key={c}
                    className="rounded-md border border-white/[0.08] bg-white/[0.03] px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                    title={c}
                  >
                    {CATEGORY_LABEL.get(c) ?? c}
                  </span>
                ))}
              </div>
            )}
          </Panel>

          {/* Agents allowed to post */}
          <Panel
            title="Agents that may post here"
            meta={agentsLoading ? undefined : String(agents.length)}
          >
            {agentsLoading ? (
              <div className="space-y-2 px-4 py-3">
                <Skeleton className="h-4 w-40 rounded" />
                <Skeleton className="h-4 w-28 rounded" />
              </div>
            ) : agents.length === 0 ? (
              <p className="px-4 py-3 text-xs text-muted-foreground">
                None. Only Crewship itself delivers here — no agent can post of its own accord.
              </p>
            ) : (
              <ul className="divide-y divide-white/[0.04]">
                {agents.map((a) => (
                  <li key={a.id} className="flex items-center gap-2 px-4 py-2 text-xs">
                    <Bot className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                    <span className="min-w-0 flex-1 truncate text-foreground/85">
                      {a.agent_name || a.agent_id}
                    </span>
                    {a.agent_slug && (
                      <span className="shrink-0 font-mono text-[10px] text-muted-foreground/60">
                        {a.agent_slug}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </Panel>
        </div>

        {/* Recent deliveries for this connection */}
        <Panel
          title="Recent deliveries"
          meta={deliveries === null ? "admins only" : String(recent.length)}
        >
          {deliveries === null ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">
              The delivery log spans every recipient in this workspace, so it needs the ADMIN or
              OWNER role.
            </p>
          ) : recent.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">
              Nothing has been sent here yet.
            </p>
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {recent.map((d) => (
                <li key={d.id} className="flex items-center gap-3 px-4 py-2 text-xs">
                  <span className="w-16 shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                    {relative(d.created_at)}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-foreground/80">
                    {d.category}
                  </span>
                  <span
                    className={cn(
                      "shrink-0 rounded-full border px-1.5 py-0.5 font-mono text-[10px]",
                      d.status === "sent"
                        ? "border-success/30 bg-success/10 text-success"
                        : d.status === "failed"
                          ? "border-destructive/35 bg-destructive/10 text-destructive"
                          : "border-warn/30 bg-warn/10 text-warn",
                    )}
                  >
                    {d.status}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  )
}

function Panel({
  title,
  meta,
  children,
}: {
  title: string
  meta?: string
  children: React.ReactNode
}) {
  return (
    <section className="overflow-hidden rounded-xl border border-white/[0.08] bg-card">
      <div className="flex items-baseline gap-2 border-b border-white/[0.06] px-4 py-2.5">
        <h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
          {title}
        </h3>
        {meta && <span className="font-mono text-[10px] text-muted-foreground/60">{meta}</span>}
      </div>
      {children}
    </section>
  )
}

/** "2 min", "3 h", "5 d". */
function relative(iso: string): string {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  const sec = Math.floor((Date.now() - t) / 1000)
  if (sec < 60) return "just now"
  if (sec < 3600) return `${Math.floor(sec / 60)} min`
  if (sec < 86400) return `${Math.floor(sec / 3600)} h`
  return `${Math.floor(sec / 86400)} d`
}
