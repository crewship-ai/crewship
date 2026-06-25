"use client"

import * as React from "react"
import Link from "next/link"
import {
  Plug,
  ShieldCheck,
  KeyRound,
  RefreshCw,
  Users,
  CheckCircle2,
  AlertCircle,
  Search,
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

type ToolkitInfo = {
  slug: string
  name: string
  meta: { description?: string; logo?: string; tools_count?: number; categories?: { name: string }[] }
}
type ToolkitsResp = { enabled: boolean; total: number; toolkits: ToolkitInfo[] }

type ComposioSettings = { configured: boolean; source: string; label?: string; base_url?: string }

function ToolkitIcon({ toolkit, size = 20 }: { toolkit: Toolkit; size?: number }) {
  // Composio logos are remote SVGs. next/image chokes on them under static
  // export, so use a plain <img> with a graceful fallback to the Plug glyph.
  const [failed, setFailed] = React.useState(false)
  if (toolkit.logo && !failed) {
    return (
      <img
        src={toolkit.logo}
        alt=""
        width={size}
        height={size}
        className="rounded object-contain"
        onError={() => setFailed(true)}
      />
    )
  }
  return (
    <span
      className="flex items-center justify-center rounded bg-blue-500/10 text-[10px] font-semibold uppercase text-blue-400"
      style={{ width: size, height: size }}
    >
      {toolkit.slug.slice(0, 2)}
    </span>
  )
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

  // ── Catalog (toolkits) ──
  const [toolkits, setToolkits] = React.useState<ToolkitInfo[]>([])
  const [total, setTotal] = React.useState(0)
  const [search, setSearch] = React.useState("")
  const [tkLoading, setTkLoading] = React.useState(true)

  const loadToolkits = React.useCallback(async (wid: string, q: string) => {
    setTkLoading(true)
    try {
      const params = new URLSearchParams({ workspace_id: wid })
      if (q) params.set("search", q)
      const r = await fetch(`/api/v1/integrations/composio/toolkits?${params}`)
      if (!r.ok) throw new Error(String(r.status))
      const j = (await r.json()) as ToolkitsResp
      setToolkits(j.toolkits ?? [])
      setTotal(j.total ?? 0)
    } catch {
      setToolkits([])
    } finally {
      setTkLoading(false)
    }
  }, [])

  // Debounce the catalog search so each keystroke doesn't hammer Composio.
  React.useEffect(() => {
    if (!workspaceId) return
    const t = setTimeout(() => void loadToolkits(workspaceId, search), 300)
    return () => clearTimeout(t)
  }, [workspaceId, search, loadToolkits])

  // Slugs already configured (auth config exists) or connected (has accounts).
  const configured = React.useMemo(() => {
    const s = new Set<string>()
    data?.auth_configs.forEach((ac) => s.add(ac.toolkit.slug))
    data?.users.forEach((u) => u.connected_accounts.forEach((a) => s.add(a.toolkit.slug)))
    return s
  }, [data])

  // ── Settings (API key) ──
  const [settings, setSettings] = React.useState<ComposioSettings | null>(null)
  const [keyOpen, setKeyOpen] = React.useState(false)

  const loadSettings = React.useCallback(async (wid: string) => {
    try {
      const r = await fetch(`/api/v1/integrations/composio/settings?workspace_id=${wid}`)
      if (r.ok) setSettings((await r.json()) as ComposioSettings)
    } catch {
      /* non-fatal */
    }
  }, [])

  React.useEffect(() => {
    if (workspaceId) void loadSettings(workspaceId)
  }, [workspaceId, loadSettings])

  const refreshAll = React.useCallback(
    (wid: string) => {
      void load(wid)
      void loadToolkits(wid, search)
      void loadSettings(wid)
    },
    [load, loadToolkits, search, loadSettings],
  )

  const busy = wsLoading || loading

  return (
    <div className="p-4 md:p-6 pb-10 space-y-5 bg-background min-h-[calc(100vh-48px)]">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <Plug className="h-4 w-4 text-foreground/60" />
          <h1 className="text-body font-medium text-foreground/80">Connectors</h1>
          <span className="text-[11px] text-muted-foreground">· powered by Composio</span>
          {settings?.configured && (
            <span className="ml-1 inline-flex items-center gap-1 rounded-full border border-emerald-400/30 bg-emerald-500/[0.08] px-2 py-0.5 text-[10px] text-emerald-400">
              ● key set{settings.source === "env" ? " (env)" : settings.label ? ` · ${settings.label}` : ""}
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => setKeyOpen(true)}>
            <KeyRound className="h-3.5 w-3.5" />
            API key
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => workspaceId && refreshAll(workspaceId)}
            disabled={busy}
          >
            <RefreshCw className={cn("h-3.5 w-3.5", busy && "animate-spin")} />
            Refresh
          </Button>
        </div>
      </div>

      {keyOpen && workspaceId && (
        <ApiKeyModal
          workspaceId={workspaceId}
          current={settings}
          onClose={() => setKeyOpen(false)}
          onChanged={() => {
            setKeyOpen(false)
            refreshAll(workspaceId)
          }}
        />
      )}

      {busy && <InventorySkeleton />}

      {!busy && error && (
        <div className="rounded-xl border border-red-500/20 bg-red-500/5 p-4 text-sm text-red-300">
          Couldn&apos;t load Composio inventory: {error}
        </div>
      )}

      {!busy && !error && data && !data.enabled && (
        <NotConfigured onAddKey={() => setKeyOpen(true)} />
      )}

      {!busy && !error && data && data.enabled && (
        <>
          <div className="rounded-lg border border-blue-400/20 bg-blue-500/[0.04] px-4 py-2.5 text-[11px] text-muted-foreground">
            Read-only preview. Connecting apps (OAuth) and assigning them to agents ship in the next update.
          </div>

          {/* Connector catalog — searchable, 1000+ apps from Composio */}
          <section className="space-y-3">
            <div className="flex items-center justify-between gap-3 flex-wrap">
              <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Connector catalog{total ? ` (${total} apps)` : ""}
              </h2>
              <div className="relative">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                <input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder="Search apps (gmail, github, slack…)"
                  className="w-64 rounded-lg border border-white/10 bg-card py-1.5 pl-8 pr-3 text-xs text-foreground placeholder:text-muted-foreground focus:border-blue-400/50 focus:outline-none"
                />
              </div>
            </div>
            {tkLoading ? (
              <div className="grid gap-3 grid-cols-2 sm:grid-cols-3 lg:grid-cols-4">
                {Array.from({ length: 8 }).map((_, i) => (
                  <Skeleton key={i} className="h-16 rounded-xl" />
                ))}
              </div>
            ) : toolkits.length === 0 ? (
              <EmptyHint text={search ? `No apps match “${search}”.` : "No apps found."} />
            ) : (
              <div className="grid gap-3 grid-cols-2 sm:grid-cols-3 lg:grid-cols-4">
                {toolkits.map((t) => {
                  const isConfigured = configured.has(t.slug)
                  return (
                    <div
                      key={t.slug}
                      className="flex items-center gap-3 rounded-xl border border-white/10 bg-card p-3"
                    >
                      <ToolkitIcon toolkit={{ slug: t.slug, logo: t.meta.logo }} />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">{t.name}</div>
                        <div className="truncate text-[11px] text-muted-foreground">
                          {t.meta.tools_count ? `${t.meta.tools_count} tools` : t.slug}
                        </div>
                      </div>
                      {isConfigured ? (
                        <span className="shrink-0 text-[10px] text-emerald-400">● connected</span>
                      ) : (
                        <button
                          type="button"
                          disabled
                          title="Connecting apps ships in the next update"
                          className="shrink-0 cursor-not-allowed rounded-lg border border-white/10 px-2 py-1 text-[11px] text-muted-foreground/60"
                        >
                          Connect
                        </button>
                      )}
                    </div>
                  )
                })}
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

function ApiKeyModal({
  workspaceId,
  current,
  onClose,
  onChanged,
}: {
  workspaceId: string
  current: ComposioSettings | null
  onClose: () => void
  onChanged: () => void
}) {
  const [apiKey, setApiKey] = React.useState("")
  const [label, setLabel] = React.useState(current?.label ?? "")
  const [saving, setSaving] = React.useState(false)
  const [err, setErr] = React.useState<string | null>(null)

  const save = async () => {
    setSaving(true)
    setErr(null)
    try {
      const r = await fetch(`/api/v1/integrations/composio/settings?workspace_id=${workspaceId}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ api_key: apiKey.trim(), base_url: current?.base_url ?? "", label: label.trim() }),
      })
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.detail || `Failed (${r.status})`)
      }
      onChanged()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    setSaving(true)
    setErr(null)
    try {
      const r = await fetch(`/api/v1/integrations/composio/settings?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (!r.ok) throw new Error(`Failed (${r.status})`)
      onChanged()
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Failed to remove")
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" onClick={onClose}>
      <div
        className="w-full max-w-md rounded-xl border border-white/10 bg-card p-6 shadow-2xl"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="text-base font-semibold text-foreground">Composio API key</h2>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          Stored encrypted for this workspace. We validate it against Composio before saving.
          From app.composio.dev → your project → Settings → API keys.
        </p>

        <div className="mt-4 space-y-3">
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">Project API key</label>
            <input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder="ak_…"
              className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 font-mono text-sm focus:border-blue-400/50 focus:outline-none"
            />
          </div>
          <div>
            <label className="mb-1 block text-xs text-muted-foreground">Label (optional)</label>
            <input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="Crewship_dev_1"
              className="w-full rounded-lg border border-white/10 bg-background px-3 py-2 text-sm focus:border-blue-400/50 focus:outline-none"
            />
          </div>
        </div>

        {err && <div className="mt-3 text-xs text-red-400">{err}</div>}

        <div className="mt-5 flex items-center justify-between gap-2">
          <div>
            {current?.source === "workspace" && (
              <Button variant="ghost" size="sm" onClick={remove} disabled={saving}>
                Remove key
              </Button>
            )}
          </div>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={onClose} disabled={saving}>
              Cancel
            </Button>
            <Button size="sm" onClick={save} disabled={saving || !apiKey.trim()}>
              {saving ? "Validating…" : "Validate & save"}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

function NotConfigured({ onAddKey }: { onAddKey: () => void }) {
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
      <h2 className="text-base font-semibold text-foreground">Connect Composio to get started</h2>
      <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
        We&apos;re replacing self-hosted MCP servers with a managed integration platform
        (Composio). Add your Composio project API key to browse 1,000+ apps and connect them.
      </p>
      <div className="mt-5">
        <Button onClick={onAddKey}>
          <KeyRound className="h-3.5 w-3.5" />
          Add API key
        </Button>
      </div>
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
