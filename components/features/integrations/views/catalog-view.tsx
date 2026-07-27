"use client"

import * as React from "react"
import { Check, Search } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { ProviderMark } from "../provider-marks"
import type {
  NotificationProvider,
  NotificationProviderCategory,
} from "@/hooks/use-notification-channels"

/**
 * The catalog — every service this instance can deliver to, in one place.
 *
 * This exists because the previous page hid the whole list inside a <Select>
 * on a form you had to already be filling in. You could not answer "what can
 * this thing even talk to?" without committing to adding something, and a
 * disabled provider was a greyed-out line you had to open the menu to find.
 *
 * Sections and their order come from the server (notify.ProviderCategories),
 * not from a mapping here, so a provider added on the backend appears in the
 * right section without a matching frontend change.
 */

/** A catalog entry, normalised across chat providers and built-in transports. */
export interface CatalogEntry {
  key: string
  label: string
  blurb: string
  /** Section key — a provider category, or one of the built-in groups below. */
  section: string
  /** false = an admin has switched it off; it cannot be picked. */
  available: boolean
  /** How many connections already use it. */
  used: number
}

/** Sections for the things that are not shoutrrr providers. */
export const BUILTIN_SECTION = "builtin"
export const TOOLS_SECTION = "tools"

/**
 * Entries the catalog adds on top of the provider registry: the two built-in
 * transports and the managed-tools card.
 *
 * Exported as a count so the sub-bar and the tab badge can say the same number
 * the catalog says. They diverged once already — the bar counted providers
 * (11) while the catalog listed 14 — and a page that contradicts itself in two
 * places a centimetre apart is not one anybody trusts.
 */
export const CATALOG_EXTRA_ENTRIES = 3

/** Total services the catalog will render for a given provider registry. */
export function catalogSize(providerCount: number): number {
  return providerCount + CATALOG_EXTRA_ENTRIES
}

const EXTRA_SECTIONS: NotificationProviderCategory[] = [
  {
    key: BUILTIN_SECTION,
    label: "E-mail & webhook",
    hint: "Built in — no third-party service involved",
  },
  {
    key: TOOLS_SECTION,
    label: "Tools (MCP)",
    hint: "What agents can call, rather than where notifications go",
  },
]

interface CatalogViewProps {
  providers: NotificationProvider[]
  categories: NotificationProviderCategory[]
  /** Connections per provider key, for the "already in use" badge. */
  usage: Record<string, number>
  loading: boolean
  search: string
  onPick: (entry: CatalogEntry) => void
  composioConfigured: boolean
}

export function CatalogView({
  providers,
  categories,
  usage,
  loading,
  search,
  onPick,
  composioConfigured,
}: CatalogViewProps) {
  const entries = React.useMemo<CatalogEntry[]>(() => {
    const fromProviders: CatalogEntry[] = providers.map((p) => ({
      key: p.provider,
      label: p.label,
      blurb: p.blurb,
      // An older server sends no category; park those in the built-in section
      // rather than dropping them — an uncategorised provider still works.
      section: p.category || BUILTIN_SECTION,
      available: p.enabled,
      used: usage[p.provider] ?? 0,
    }))
    return [
      ...fromProviders,
      {
        key: "email",
        label: "E-mail",
        // Deliberately not gated on a "mail is configured" flag: no endpoint
        // reports one, and inferring it would be a guess rendered as fact.
        // An instance with no transport rejects the create with its own
        // message, which the dialog surfaces verbatim.
        blurb: "Send to any address, using this instance's mail transport",
        section: BUILTIN_SECTION,
        available: true,
        used: usage.email ?? 0,
      },
      {
        key: "webhook",
        label: "Webhook",
        blurb: "A signed POST to an endpoint you control",
        section: BUILTIN_SECTION,
        available: true,
        used: usage.webhook ?? 0,
      },
      {
        key: "composio",
        label: "Composio",
        blurb: composioConfigured
          ? "Managed tool accounts your agents can call"
          : "Needs an API key before agents can connect accounts",
        section: TOOLS_SECTION,
        available: composioConfigured,
        used: usage.composio ?? 0,
      },
    ]
  }, [providers, usage, composioConfigured])

  const q = search.trim().toLowerCase()
  const sections = React.useMemo(
    () => [...categories, ...EXTRA_SECTIONS],
    [categories],
  )

  const matching = entries.filter(
    (e) =>
      !q ||
      e.label.toLowerCase().includes(q) ||
      e.blurb.toLowerCase().includes(q) ||
      e.key.toLowerCase().includes(q) ||
      (sections.find((s) => s.key === e.section)?.label ?? "").toLowerCase().includes(q),
  )

  if (loading) {
    return (
      <div className="space-y-4 p-4 md:p-6">
        <Skeleton className="h-5 w-40 rounded" />
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-[70px] rounded-xl" />
          ))}
        </div>
      </div>
    )
  }

  const readyCount = matching.filter((e) => e.available).length

  return (
    <div className="space-y-5 p-4 md:p-6">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <h2 className="text-sm font-medium text-foreground/90">Catalog</h2>
        <span className="text-xs text-muted-foreground">
          {matching.length} {matching.length === 1 ? "service" : "services"} · {readyCount} ready to
          use
        </span>
        <span className="flex-1" />
        {q && (
          <span className="text-[11px] text-muted-foreground/70">
            filtered by “{search.trim()}”
          </span>
        )}
      </div>

      {matching.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-xl border border-white/[0.08] bg-card px-6 py-14 text-center">
          <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-lg bg-white/[0.04]">
            <Search className="h-4 w-4 text-muted-foreground/60" />
          </div>
          <div className="text-sm font-medium text-foreground/85">
            Nothing here matches “{search.trim()}”
          </div>
          <p className="mt-1 max-w-sm text-xs text-muted-foreground">
            This instance ships {entries.length} services. Clear the search to see all of them.
          </p>
        </div>
      ) : (
        sections.map((section) => {
          const items = matching.filter((e) => e.section === section.key)
          if (items.length === 0) return null
          return (
            <section key={section.key} className="space-y-2">
              <div className="flex items-baseline gap-2">
                <h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
                  {section.label}
                </h3>
                <span className="tabular-nums text-[10px] text-muted-foreground/50">
                  {items.length}
                </span>
                {section.hint && (
                  <span className="hidden text-[11px] text-muted-foreground/60 sm:inline">
                    · {section.hint}
                  </span>
                )}
              </div>
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {items.map((entry) => (
                  <CatalogCard key={entry.key} entry={entry} onPick={() => onPick(entry)} />
                ))}
              </div>
            </section>
          )
        })
      )}
    </div>
  )
}

function CatalogCard({ entry, onPick }: { entry: CatalogEntry; onPick: () => void }) {
  return (
    <button
      type="button"
      onClick={onPick}
      disabled={!entry.available}
      title={
        entry.available
          ? `Connect ${entry.label}`
          : `${entry.label} is not available on this instance`
      }
      className={cn(
        "group flex w-full items-start gap-3 rounded-xl border bg-card px-3.5 py-3 text-left transition-colors",
        entry.available
          ? "border-white/[0.08] hover:border-primary/40 hover:bg-white/[0.02]"
          : "cursor-not-allowed border-white/[0.05] opacity-45",
      )}
    >
      <ProviderMark provider={entry.key} label={entry.label} className="mt-0.5" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium text-foreground/90">{entry.label}</span>
        <span className="mt-0.5 block text-[11px] leading-snug text-muted-foreground">
          {entry.blurb}
        </span>
      </span>
      <span className="shrink-0 self-center">
        {entry.used > 0 ? (
          <span className="inline-flex items-center gap-1 rounded-full border border-emerald-400/25 bg-emerald-400/10 px-1.5 py-0.5 font-mono text-[10px] text-emerald-300">
            <Check className="h-2.5 w-2.5" />
            {entry.used}
          </span>
        ) : entry.available ? (
          <span className="font-mono text-[10px] text-muted-foreground/50">ready</span>
        ) : (
          <span className="font-mono text-[10px] text-muted-foreground/50">off</span>
        )}
      </span>
    </button>
  )
}
