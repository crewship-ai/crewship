"use client"

import * as React from "react"
import Link from "next/link"
import Image from "next/image"
import {
  Plug,
  ShieldCheck,
  KeyRound,
  RefreshCw,
  Users,
  CheckCircle2,
  AlertCircle,
} from "lucide-react"

import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

// Managed-integration (Composio) view rendered at /integrations. Slice 2a
// shipped the backend; this is the first UI iteration — a read-only inventory
// (connector catalog + connected accounts grouped by Composio user_id) fetched
// from GET /api/v1/integrations/composio/inventory. Connecting apps (OAuth) and
// per-agent binding land in follow-up slices.

type Toolkit = { slug: string; logo?: string }
type AuthConfig = { id: string; name: string; status: string; toolkit: Toolkit }
type ConnectedAccount = { id: string; user_id: string; status: string; toolkit: Toolkit }
type UserInventory = { user_id: string; connected_accounts: ConnectedAccount[] }
type Inventory = { enabled: boolean; auth_configs: AuthConfig[]; users: UserInventory[] }

function ToolkitIcon({ toolkit, size = 20 }: { toolkit: Toolkit; size?: number }) {
  if (toolkit.logo) {
    return (
      <Image
        src={toolkit.logo}
        alt={toolkit.slug}
        width={size}
        height={size}
        className="rounded"
        unoptimized
      />
    )
  }
  return <Plug className="text-blue-400" style={{ width: size, height: size }} />
}

function StatusDot({ status }: { status: string }) {
  const ok = status?.toUpperCase() === "ACTIVE" || status?.toUpperCase() === "ENABLED"
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 text-[11px]",
        ok ? "text-emerald-400" : "text-amber-400",
      )}
    >
      {ok ? <CheckCircle2 className="h-3 w-3" /> : <AlertCircle className="h-3 w-3" />}
      {status}
    </span>
  )
}

export function ComposioIntegrations() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [data, setData] = React.useState<Inventory | null>(null)
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState<string | null>(null)

  const load = React.useCallback(async (wid: string) => {
    setLoading(true)
    setError(null)
    try {
      const r = await fetch(`/api/v1/integrations/composio/inventory?workspace_id=${wid}`)
      if (!r.ok) throw new Error(`Request failed (${r.status})`)
      setData((await r.json()) as Inventory)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load inventory")
    } finally {
      setLoading(false)
    }
  }, [])

  React.useEffect(() => {
    if (workspaceId) void load(workspaceId)
  }, [workspaceId, load])

  const busy = wsLoading || loading

  return (
    <div className="p-4 md:p-6 pb-10 space-y-5 bg-background min-h-[calc(100vh-48px)]">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <Plug className="h-4 w-4 text-foreground/60" />
          <h1 className="text-body font-medium text-foreground/80">Connectors</h1>
          <span className="text-[11px] text-muted-foreground">· powered by Composio</span>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => workspaceId && load(workspaceId)}
          disabled={busy}
        >
          <RefreshCw className={cn("h-3.5 w-3.5", busy && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {busy && <InventorySkeleton />}

      {!busy && error && (
        <div className="rounded-xl border border-red-500/20 bg-red-500/5 p-4 text-sm text-red-300">
          Couldn&apos;t load Composio inventory: {error}
        </div>
      )}

      {!busy && !error && data && !data.enabled && <NotConfigured />}

      {!busy && !error && data && data.enabled && (
        <>
          <div className="rounded-lg border border-blue-400/20 bg-blue-500/[0.04] px-4 py-2.5 text-[11px] text-muted-foreground">
            Read-only preview. Connecting apps (OAuth) and assigning them to agents ship in the next update.
          </div>

          {/* Connector catalog */}
          <section className="space-y-3">
            <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              Connectable apps ({data.auth_configs.length})
            </h2>
            {data.auth_configs.length === 0 ? (
              <EmptyHint text="No connectors configured in this Composio project yet." />
            ) : (
              <div className="grid gap-3 grid-cols-2 sm:grid-cols-3 lg:grid-cols-4">
                {data.auth_configs.map((ac) => (
                  <div
                    key={ac.id}
                    className="flex items-center gap-3 rounded-xl border border-white/10 bg-card p-3"
                  >
                    <ToolkitIcon toolkit={ac.toolkit} />
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-medium capitalize truncate">{ac.toolkit.slug}</div>
                      <StatusDot status={ac.status} />
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>

          {/* Connected accounts grouped by user */}
          <section className="space-y-3">
            <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              <Users className="h-3.5 w-3.5" /> Connected users ({data.users.length})
            </h2>
            {data.users.length === 0 ? (
              <EmptyHint text="No connected accounts yet. Once a user connects an app, it appears here." />
            ) : (
              <div className="space-y-2">
                {data.users.map((u) => (
                  <div
                    key={u.user_id}
                    className="rounded-xl border border-white/10 bg-card p-3"
                  >
                    <div className="flex items-center justify-between gap-3">
                      <div className="font-mono text-xs text-foreground/90 truncate">{u.user_id}</div>
                      <span className="shrink-0 text-[10px] text-muted-foreground">
                        {u.connected_accounts.length} account{u.connected_accounts.length === 1 ? "" : "s"}
                      </span>
                    </div>
                    <div className="mt-2 flex flex-wrap gap-2">
                      {u.connected_accounts.map((a) => (
                        <span
                          key={a.id}
                          className="inline-flex items-center gap-1.5 rounded-lg border border-white/10 bg-white/[0.03] px-2 py-1 text-[11px]"
                        >
                          <ToolkitIcon toolkit={a.toolkit} size={14} />
                          <span className="capitalize">{a.toolkit.slug}</span>
                          <StatusDot status={a.status} />
                        </span>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function InventorySkeleton() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-8 w-64" />
      <div className="grid gap-3 grid-cols-2 sm:grid-cols-4">
        {Array.from({ length: 4 }).map((_, i) => (
          <Skeleton key={i} className="h-16 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-20 rounded-xl" />
    </div>
  )
}

function EmptyHint({ text }: { text: string }) {
  return (
    <div className="rounded-xl border border-dashed border-white/10 p-4 text-[11px] text-muted-foreground">
      {text}
    </div>
  )
}

function NotConfigured() {
  return (
    <div
      className={cn(
        "mx-auto mt-8 max-w-xl rounded-xl border border-white/10 bg-card p-8 text-center",
        "shadow-lg shadow-blue-500/5",
      )}
    >
      <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-blue-500/10">
        <Plug className="h-6 w-6 text-blue-400" />
      </div>
      <h2 className="text-base font-semibold text-foreground">Managed integrations are coming soon</h2>
      <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
        We&apos;re replacing self-hosted MCP servers with a managed integration platform
        (Composio). Set <code className="text-foreground/80">COMPOSIO_API_KEY</code> on the
        server to enable it.
      </p>
      <div className="mt-6 grid gap-3 text-left sm:grid-cols-2">
        <div className="rounded-lg border border-white/10 bg-white/[0.02] p-3">
          <ShieldCheck className="h-4 w-4 text-blue-400" />
          <div className="mt-2 text-xs font-medium text-foreground/90">Per-user OAuth</div>
          <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
            Each agent acts on behalf of the connected user — no shared secrets.
          </div>
        </div>
        <div className="rounded-lg border border-white/10 bg-white/[0.02] p-3">
          <Plug className="h-4 w-4 text-blue-400" />
          <div className="mt-2 text-xs font-medium text-foreground/90">Hundreds of apps</div>
          <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
            A single managed catalog replaces hand-configured MCP endpoints.
          </div>
        </div>
      </div>
      <div className="mt-6 flex items-center justify-center">
        <Link
          href="/credentials"
          className="inline-flex items-center gap-1.5 text-xs text-blue-400 transition-all hover:gap-2.5"
        >
          <KeyRound className="h-3.5 w-3.5" />
          Manage credentials in the meantime
        </Link>
      </div>
    </div>
  )
}
