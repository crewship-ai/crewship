"use client"

import * as React from "react"
import { motion } from "motion/react"
import { Search, Plus, Globe, Terminal } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { MCPLogo } from "@/components/icons/mcp-logos"
import { TrustTierBadge, type TrustTier } from "./trust-tier-badge"

interface RegistryEntry {
  id: string
  name: string
  display_name: string
  description: string
  icon: string
  transport: string
  homepage_url: string
  source_url: string
  package_name: string
  package_registry: string
  command: string
  endpoint: string
  auth_type: string
  env_vars_json: string
  category: string
  is_verified: boolean
  trust_tier: TrustTier
  is_featured: boolean
  synced_at: string
}

interface RegistryResponse {
  servers: RegistryEntry[]
  total: number
  limit: number
  offset: number
}

export interface MarketplaceProps {
  onAdd: (entry: RegistryEntry) => void
  /** When set, the empty-state shows recipe cards instead of a flat message. */
  recipeEmptyState?: React.ReactNode
}

export function Marketplace({ onAdd, recipeEmptyState }: MarketplaceProps) {
  const [query, setQuery] = React.useState("")
  const [debouncedQuery, setDebouncedQuery] = React.useState("")
  const [transport, setTransport] = React.useState<"all" | "stdio" | "streamable-http">("all")
  const [trust, setTrust] = React.useState<"all" | TrustTier>("all")
  const [category, setCategory] = React.useState<string | null>(null)
  const [servers, setServers] = React.useState<RegistryEntry[]>([])
  const [featured, setFeatured] = React.useState<RegistryEntry[]>([])
  const [loading, setLoading] = React.useState(true)
  const [total, setTotal] = React.useState(0)
  const [installing, setInstalling] = React.useState<string | null>(null)
  const abortRef = React.useRef<AbortController | null>(null)

  // Debounce search
  React.useEffect(() => {
    const t = setTimeout(() => setDebouncedQuery(query), 300)
    return () => clearTimeout(t)
  }, [query])

  // Initial featured fetch
  React.useEffect(() => {
    fetch(`/api/v1/mcp-registry?featured=true&limit=10`)
      .then((r) => r.ok ? r.json() : null)
      .then((data: RegistryResponse | null) => setFeatured(data?.servers ?? []))
      .catch(() => {})
  }, [])

  // Main list fetch
  React.useEffect(() => {
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)

    const params = new URLSearchParams({ limit: "200" })
    if (trust !== "all") params.set("trust_tier", trust)
    const url = debouncedQuery.trim()
      ? `/api/v1/mcp-registry/search?q=${encodeURIComponent(debouncedQuery.trim())}&${params}`
      : `/api/v1/mcp-registry?${params}`

    fetch(url, { signal: ctrl.signal })
      .then((r) => r.ok ? r.json() : null)
      .then((data: RegistryResponse | null) => {
        if (!data) { setServers([]); setTotal(0); return }
        setServers(data.servers)
        setTotal(data.total)
      })
      .catch((err) => {
        if (err instanceof DOMException && err.name === "AbortError") return
        setServers([])
      })
      .finally(() => setLoading(false))
    return () => ctrl.abort()
  }, [debouncedQuery, trust])

  // Categories from current server set (so they auto-update with filters)
  const categoryCounts = React.useMemo(() => {
    const counts = new Map<string, number>()
    for (const s of servers) {
      const c = s.category || "uncategorised"
      counts.set(c, (counts.get(c) ?? 0) + 1)
    }
    return Array.from(counts.entries()).sort((a, b) => b[1] - a[1])
  }, [servers])

  const filtered = React.useMemo(() => {
    return servers.filter((s) => {
      if (transport !== "all") {
        if (transport === "stdio" && s.transport !== "stdio") return false
        if (transport === "streamable-http" && s.transport !== "streamable-http") return false
      }
      if (category && s.category !== category) return false
      return true
    })
  }, [servers, transport, category])

  const trustCounts = React.useMemo(() => {
    let a = 0, c = 0, comm = 0
    for (const s of servers) {
      if (s.trust_tier === "anthropic") a++
      else if (s.trust_tier === "crewship") c++
      else comm++
    }
    return { anthropic: a, crewship: c, community: comm }
  }, [servers])

  return (
    <div className="grid grid-cols-[180px_1fr] gap-4 min-h-[600px]">
      {/* Left sidebar: categories + verified-by sub-filter */}
      <div className="space-y-4">
        <div>
          <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium px-2 py-1.5">Categories</div>
          <button
            onClick={() => setCategory(null)}
            className={cn(
              "w-full flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-xs transition-colors",
              category === null ? "bg-blue-500/10 text-blue-300" : "text-foreground/80 hover:bg-white/[0.02]",
            )}
          >
            <span>All</span>
            <span className="text-[10px] font-mono opacity-60">{servers.length}</span>
          </button>
          {categoryCounts.map(([cat, n]) => (
            <button
              key={cat}
              onClick={() => setCategory(cat === category ? null : cat)}
              className={cn(
                "w-full flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-xs transition-colors capitalize",
                category === cat ? "bg-blue-500/10 text-blue-300" : "text-foreground/80 hover:bg-white/[0.02]",
              )}
            >
              <span className="truncate">{cat.replace(/-/g, " ")}</span>
              <span className="text-[10px] font-mono opacity-60">{n}</span>
            </button>
          ))}
        </div>

        <div>
          <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium px-2 py-1.5">Verified by</div>
          {(["all", "anthropic", "crewship", "community"] as const).map((t) => (
            <button
              key={t}
              onClick={() => setTrust(t)}
              className={cn(
                "w-full flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-xs transition-colors capitalize",
                trust === t ? "bg-blue-500/10 text-blue-300" : "text-foreground/80 hover:bg-white/[0.02]",
              )}
            >
              <span>{t === "all" ? "All" : t}</span>
              {t !== "all" && (
                <span className="text-[10px] font-mono opacity-60">{trustCounts[t]}</span>
              )}
            </button>
          ))}
        </div>
      </div>

      {/* Right content */}
      <div className="space-y-4 min-w-0">
        {/* Search + filter row */}
        <div className="flex items-center gap-2 flex-wrap">
          <div className="relative flex-1 min-w-[260px]">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
            <Input
              placeholder={`Search ${total} servers...`}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="pl-8 h-8"
            />
          </div>
          {(["all", "stdio", "streamable-http"] as const).map((t) => (
            <button
              key={t}
              onClick={() => setTransport(t)}
              className={cn(
                "h-8 px-3 rounded-md text-xs font-medium border transition-colors",
                transport === t
                  ? "bg-blue-500/10 border-blue-400/30 text-blue-300"
                  : "border-white/10 text-muted-foreground hover:bg-white/[0.02]",
              )}
            >
              {t === "all" ? "All transports" : t === "stdio" ? "stdio" : "HTTP"}
            </button>
          ))}
        </div>

        {/* Featured row */}
        {featured.length > 0 && !debouncedQuery && (
          <div className="space-y-2">
            <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">Featured</div>
            <div className="grid gap-2 grid-cols-2 lg:grid-cols-3">
              {featured.slice(0, 6).map((s) => (
                <FeaturedCard key={s.id} entry={s} onAdd={onAdd} installing={installing === s.id} />
              ))}
            </div>
          </div>
        )}

        {/* Main grid */}
        <div className="space-y-2">
          <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
            {filtered.length} server{filtered.length === 1 ? "" : "s"}
          </div>
          {loading ? (
            <div className="flex items-center justify-center py-12">
              <Spinner className="h-5 w-5 text-muted-foreground" />
            </div>
          ) : filtered.length === 0 ? (
            recipeEmptyState ?? (
              <div className="rounded-md border border-white/10 bg-zinc-950 p-12 text-center text-sm text-muted-foreground">
                {debouncedQuery ? "No servers match your search." : "Registry empty — wait for first sync."}
              </div>
            )
          ) : (
            <div className="grid gap-2 grid-cols-1 lg:grid-cols-2 xl:grid-cols-3">
              {filtered.map((s, idx) => (
                <motion.div
                  key={s.id}
                  initial={{ opacity: 0, y: 4 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ duration: 0.12, delay: Math.min(idx, 20) * 0.01 }}
                >
                  <Card
                    entry={s}
                    onAdd={() => { setInstalling(s.id); onAdd(s); setInstalling(null) }}
                    installing={installing === s.id}
                  />
                </motion.div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

function FeaturedCard({ entry, onAdd, installing }: { entry: RegistryEntry; onAdd: (e: RegistryEntry) => void; installing: boolean }) {
  return (
    <button
      type="button"
      onClick={() => onAdd(entry)}
      disabled={installing}
      className="group flex items-start gap-3 rounded-xl border border-white/10 bg-card p-4 text-left transition-all hover:border-blue-400/40 hover:bg-white/[0.02]"
    >
      <MCPLogo name={entry.icon || entry.name} transport={entry.transport} className="h-10 w-10 shrink-0 opacity-90" />
      <div className="flex-1 min-w-0 space-y-1">
        <div className="flex items-center gap-1.5">
          <span className="text-sm font-semibold truncate">{entry.display_name || entry.name}</span>
          {entry.is_featured && (
            <Badge variant="outline" className="text-[10px] h-4 px-1 border-amber-400/40 text-amber-300">★</Badge>
          )}
        </div>
        <p className="text-[11px] text-muted-foreground line-clamp-2 leading-relaxed">{entry.description}</p>
        <div className="flex items-center gap-1.5 pt-0.5">
          <TrustTierBadge tier={entry.trust_tier} />
        </div>
      </div>
    </button>
  )
}

function Card({ entry, onAdd, installing }: { entry: RegistryEntry; onAdd: () => void; installing: boolean }) {
  return (
    <div className="rounded-md border border-white/10 bg-zinc-950 p-3 hover:border-white/20 transition-colors">
      <div className="flex items-start gap-2.5">
        <MCPLogo name={entry.icon || entry.name} transport={entry.transport} className="h-6 w-6 shrink-0 mt-0.5 opacity-85" />
        <div className="flex-1 min-w-0 space-y-1">
          <div className="flex items-center gap-1.5 flex-wrap">
            <span className="text-xs font-medium truncate">{entry.display_name || entry.name}</span>
            <Badge variant="outline" className="text-[10px] h-4 px-1 gap-0.5">
              {entry.transport === "stdio" ? <Terminal className="h-2 w-2" /> : <Globe className="h-2 w-2" />}
              {entry.transport === "stdio" ? "stdio" : "HTTP"}
            </Badge>
            {entry.category && (
              <Badge variant="secondary" className="text-[10px] h-4 px-1 capitalize">
                {entry.category.replace(/-/g, " ")}
              </Badge>
            )}
          </div>
          {entry.description && (
            <p className="text-[11px] text-muted-foreground line-clamp-2 leading-relaxed">{entry.description}</p>
          )}
          <div className="flex items-center justify-between gap-2 pt-1">
            <TrustTierBadge tier={entry.trust_tier} />
            <Button
              variant="outline"
              size="sm"
              className="h-6 text-[11px] px-2"
              disabled={installing}
              onClick={onAdd}
            >
              {installing ? (
                <Spinner className="h-3 w-3" />
              ) : (
                <>
                  <Plus className="mr-0.5 h-3 w-3" />
                  Install
                </>
              )}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
