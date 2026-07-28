"use client"

import * as React from "react"
import { Bot, Globe, Mail, MessageSquare, Plug, Send, Siren, Smartphone, Trash2 } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { ProviderMark } from "../provider-marks"
import {
  STATUS_LABEL,
  type ConnectionKind,
  type ConnectionRow,
  type ConnectionStatus,
} from "../connection-model"

const KIND_ICON: Record<ConnectionKind, LucideIcon> = {
  chat: MessageSquare,
  push: Smartphone,
  incident: Siren,
  email: Mail,
  webhook: Globe,
  tools: Bot,
}

const STATUS_STYLE: Record<ConnectionStatus, string> = {
  delivering: "border-success/30 bg-success/10 text-success",
  failing: "border-destructive/35 bg-destructive/10 text-destructive",
  never_used: "border-warn/30 bg-warn/10 text-warn",
  disabled: "border-white/10 bg-white/[0.03] text-muted-foreground",
  unknown: "border-info/25 bg-info/10 text-info",
}

interface ConnectionsViewProps {
  rows: ConnectionRow[]
  /** Rows before search/facets — drives the "N hidden by filters" line. */
  totalRows: number
  loading: boolean
  error: string | null
  /** Null when the caller may not read the delivery log (see ConnectionStatus). */
  canSeeDeliveries: boolean
  canManageWorkspace: boolean
  search: string
  /** Opens the Add-integration flow — there is no catalog tab to send them to. */
  onOpenAdd: () => void
  onToggleEnabled: (row: ConnectionRow, next: boolean) => Promise<void>
  onTest: (row: ConnectionRow) => Promise<void>
  onDelete: (row: ConnectionRow) => Promise<void>
  /** Opens a row's detail. The left panel selects the same thing. */
  onSelect: (id: string) => void
  /** Services matching the current search — the "not here, but addable" hint. */
  catalogMatches: number
}

export function ConnectionsView({
  rows,
  totalRows,
  loading,
  error,
  canSeeDeliveries,
  canManageWorkspace,
  search,
  onOpenAdd,
  onToggleEnabled,
  onTest,
  onDelete,
  onSelect,
  catalogMatches,
}: ConnectionsViewProps) {
  const [busyId, setBusyId] = React.useState<string | null>(null)
  const [action, setAction] = React.useState<"toggle" | "test" | "delete" | null>(null)

  const run = async (row: ConnectionRow, kind: "toggle" | "test" | "delete", fn: () => Promise<void>) => {
    setBusyId(row.id)
    setAction(kind)
    try {
      await fn()
    } finally {
      setBusyId(null)
      setAction(null)
    }
  }

  if (loading) {
    return (
      <div className="space-y-4 p-4 md:p-6">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-[92px] rounded-xl" />
          ))}
        </div>
        <Skeleton className="h-[260px] rounded-xl" />
      </div>
    )
  }

  const delivering = rows.filter((r) => r.status === "delivering").length
  const failing = rows.filter((r) => r.status === "failing").length
  const sent24h = rows.reduce((a, r) => a + (r.sent24h ?? 0), 0)
  const hiddenByFilters = totalRows - rows.length

  return (
    <div className="space-y-4 p-4 md:p-6">
      {error && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/[0.06] px-3 py-2 text-xs text-destructive">
          {error}
        </div>
      )}

      {/* KPI strip — the summary before the detail. `canSeeDeliveries` gates
          the two cards that can only be answered from the delivery log; a
          member is told they cannot see it rather than shown a plausible 0. */}
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <KpiCard
          label="Connections"
          value={rows.length}
          subtitle={
            hiddenByFilters > 0
              ? `${hiddenByFilters} hidden by filters`
              : `${rows.filter((r) => r.scope === "workspace").length} workspace · ${rows.filter((r) => r.scope === "personal").length} personal`
          }
        />
        <KpiCard
          label="Delivering"
          value={canSeeDeliveries ? delivering : "—"}
          valueColor={canSeeDeliveries && delivering > 0 ? "rgb(52, 211, 153)" : undefined}
          subtitle={canSeeDeliveries ? `of ${rows.length}` : "admins only"}
        />
        <KpiCard
          label="Needs attention"
          value={canSeeDeliveries ? failing : "—"}
          valueColor={canSeeDeliveries && failing > 0 ? "rgb(248, 113, 113)" : undefined}
          subtitle={
            !canSeeDeliveries ? "admins only" : failing > 0 ? "failing right now" : "all healthy"
          }
        />
        <KpiCard
          label="Sent · 24h"
          value={canSeeDeliveries ? sent24h : "—"}
          subtitle={canSeeDeliveries ? "across every connection" : "admins only"}
        />
      </div>

      {rows.length === 0 ? (
        <EmptyConnections
          search={search}
          totalRows={totalRows}
          catalogMatches={catalogMatches}
          onOpenAdd={onOpenAdd}
        />
      ) : (
        <div className="overflow-hidden rounded-xl border border-white/[0.08] bg-card">
          <div className="flex items-center gap-2 border-b border-white/[0.06] px-4 py-2.5">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
              Connections
            </span>
            <span className="font-mono text-[10px] text-muted-foreground/60">
              {rows.length} shown
            </span>
          </div>

          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-xs">
              <thead>
                <tr className="border-b border-white/[0.06]">
                  <Th>Connection</Th>
                  <Th>Kind</Th>
                  <Th>Scope</Th>
                  <Th>Categories</Th>
                  {canSeeDeliveries && <Th className="text-right">Sent 24h</Th>}
                  <Th>Status</Th>
                  <Th className="text-right">Actions</Th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const KindIcon = KIND_ICON[row.kind]
                  const busy = busyId === row.id
                  return (
                    <tr
                      key={row.id}
                      className="border-b border-white/[0.04] last:border-0 hover:bg-white/[0.02]"
                    >
                      <td className="px-4 py-2.5">
                        {/* The whole identity cell opens the detail. The action
                            cell stays outside it so Test and Delete are not
                            nested inside a row-sized button. */}
                        <button
                          type="button"
                          onClick={() => onSelect(row.id)}
                          className="flex w-full items-center gap-2.5 text-left"
                          title={`Open ${row.name}`}
                        >
                          {/* The service's own mark, not the KIND icon: you
                              recognise Slack by its colour long before you
                              read the row. The kind stays its own column. */}
                          <ProviderMark
                            provider={row.provider}
                            label={row.providerLabel}
                            className="h-6 w-6"
                          />
                          <div className="min-w-0">
                            <div className="truncate font-medium text-foreground/90">{row.name}</div>
                            <div className="truncate font-mono text-[10px] text-muted-foreground/70">
                              {row.detail}
                            </div>
                          </div>
                        </button>
                      </td>
                      <td className="px-4 py-2.5 text-[11px] text-muted-foreground">
                        <span className="inline-flex items-center gap-1.5">
                          <KindIcon className="h-3 w-3 shrink-0 opacity-70" />
                          <span className="font-mono">{row.kind}</span>
                        </span>
                      </td>
                      <td className="px-4 py-2.5 text-[11px] text-muted-foreground">{row.scope}</td>
                      <td className="max-w-[16rem] px-4 py-2.5 font-mono text-[10px] text-muted-foreground">
                        <span className="block truncate" title={categoryTitle(row)}>
                          {row.categories.length === 0 ? "every category" : row.categories.join(", ")}
                        </span>
                      </td>
                      {canSeeDeliveries && (
                        <td className="px-4 py-2.5 text-right font-mono tabular-nums text-[11px] text-muted-foreground">
                          {row.sent24h ?? "—"}
                        </td>
                      )}
                      <td className="px-4 py-2.5">
                        <span
                          className={cn(
                            "inline-flex items-center rounded-full border px-2 py-0.5 text-[10px]",
                            STATUS_STYLE[row.status],
                          )}
                        >
                          {STATUS_LABEL[row.status]}
                        </span>
                      </td>
                      <td className="px-4 py-2.5">
                        <div className="flex items-center justify-end gap-1">
                          {row.readOnly ? (
                            <span
                              className="text-[10px] text-muted-foreground/60"
                              title={
                                row.source === "composio"
                                  ? "Managed accounts are connected and revoked from the Composio surface"
                                  : "Another member's personal connection — only they can change it"
                              }
                            >
                              {row.source === "composio" ? "managed in Tools" : "not yours to edit"}
                            </span>
                          ) : (
                            <>
                              <Switch
                                size="sm"
                                checked={row.enabled}
                                disabled={busy}
                                onCheckedChange={(next) =>
                                  run(row, "toggle", () => onToggleEnabled(row, next))
                                }
                                aria-label={row.enabled ? `Disable ${row.name}` : `Enable ${row.name}`}
                              />
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-6 px-2 text-[11px] text-muted-foreground hover:text-foreground"
                                disabled={busy}
                                onClick={() => run(row, "test", () => onTest(row))}
                              >
                                {busy && action === "test" ? (
                                  <Spinner className="mr-1 size-3" />
                                ) : (
                                  <Send className="mr-1 size-3" />
                                )}
                                Test
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon"
                                className="h-6 w-6 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                                disabled={busy}
                                aria-label={`Delete ${row.name}`}
                                onClick={() => run(row, "delete", () => onDelete(row))}
                              >
                                {busy && action === "delete" ? (
                                  <Spinner className="size-3" />
                                ) : (
                                  <Trash2 className="size-3" />
                                )}
                              </Button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>

          <div className="border-t border-white/[0.06] px-4 py-2 text-[10px] text-muted-foreground/70">
            {canManageWorkspace
              ? "Workspace connections are shared with everyone here; personal ones are yours alone."
              : "You can add and manage your own personal connections. Workspace-wide ones need ADMIN or OWNER."}
          </div>
        </div>
      )}
    </div>
  )
}

function categoryTitle(row: ConnectionRow): string {
  return row.categories.length === 0
    ? "This connection may carry every category"
    : row.categories.join(", ")
}

function Th({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <th
      className={cn(
        "whitespace-nowrap px-4 py-2 text-left text-[10px] font-semibold uppercase tracking-wider text-foreground/45",
        className,
      )}
    >
      {children}
    </th>
  )
}

function EmptyConnections({
  search,
  totalRows,
  catalogMatches,
  onOpenAdd,
}: {
  search: string
  totalRows: number
  catalogMatches: number
  /** Opens the Add-integration flow — there is no catalog tab to send them to. */
  onOpenAdd: () => void
}) {
  const filtered = totalRows > 0
  return (
    <div className="flex flex-col items-center justify-center rounded-xl border border-white/[0.08] bg-card px-6 py-14 text-center">
      <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-lg bg-white/[0.04]">
        <Plug className="h-4 w-4 text-muted-foreground/60" />
      </div>
      <div className="text-sm font-medium text-foreground/85">
        {filtered ? "Nothing matches those filters" : "No connections yet"}
      </div>
      <p className="mt-1 max-w-sm text-xs leading-relaxed text-muted-foreground">
        {filtered
          ? "Clear a facet in the sidebar, or add a service you have not connected yet."
          : "A connection is where Crewship reaches you — a chat room, a phone, an on-call rota, an inbox or your own endpoint."}
      </p>
      {/* The honest answer to "I searched for telegram and got nothing": say
          where the matches actually are instead of showing a bare empty list. */}
      {search.trim() && catalogMatches > 0 && (
        <p className="mt-3 text-xs text-muted-foreground">
          {catalogMatches} {catalogMatches === 1 ? "service matches" : "services match"}{" "}
          <span className="font-medium text-foreground/80">“{search.trim()}”</span> and can be added.
        </p>
      )}
      <Button variant="soft" size="sm" className="mt-4 h-7 gap-1.5 text-xs" onClick={onOpenAdd}>
        <Plug className="h-3 w-3" />
        Add an integration
      </Button>
    </div>
  )
}
