"use client"

import * as React from "react"
import { Search } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { ToolkitIcon, EmptyHint } from "./shared"
import type { ToolkitInfo } from "./types"

// CatalogTab — the searchable connector catalog (1000+ Composio apps). Search
// is owned by the parent (debounced + shared with the global refresh), so this
// is a controlled presentational tab. Connect/+Account routes back up to the
// shared ConnectModal.
export function CatalogTab({
  toolkits,
  total,
  search,
  onSearch,
  loading,
  configuredSlugs,
  onConnect,
}: {
  toolkits: ToolkitInfo[]
  total: number
  search: string
  onSearch: (q: string) => void
  loading: boolean
  configuredSlugs: Set<string>
  onConnect: (toolkit: { slug: string; name: string }) => void
}) {
  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Connector catalog{total ? ` (${total} apps)` : ""}
        </h2>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => onSearch(e.target.value)}
            placeholder="Search apps (gmail, github, slack…)"
            className="w-64 rounded-lg border border-white/10 bg-card py-1.5 pl-8 pr-3 text-xs text-foreground placeholder:text-muted-foreground focus:border-blue-400/50 focus:outline-none"
          />
        </div>
      </div>
      {loading ? (
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
            const isConfigured = configuredSlugs.has(t.slug)
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
                <button
                  type="button"
                  onClick={() => onConnect({ slug: t.slug, name: t.name })}
                  className={cn(
                    "shrink-0 rounded-lg border px-2 py-1 text-[11px] transition-colors",
                    isConfigured
                      ? "border-emerald-400/30 text-emerald-400 hover:bg-emerald-500/10"
                      : "border-white/10 text-foreground/80 hover:border-blue-400/50 hover:text-blue-400",
                  )}
                >
                  {isConfigured ? "+ Account" : "Connect"}
                </button>
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
