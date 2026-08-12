"use client"

/**
 * The credentials landing pane.
 *
 * What was here: the KPI strip and then, immediately, a table of every secret in
 * the workspace — the same list the rail on the left already shows, iconed,
 * searchable and filtered, except that this copy was the one you could not
 * search. /routines had the identical problem and answered it by giving the
 * landing pane to a dashboard; this is that answer for the vault.
 *
 * It reports, in the order someone asks on arrival: how much of this is guarded
 * and how much is wide open, what is broken, what shape the vault has, and what
 * is actually being used. Selecting a credential in the rail replaces it with
 * that credential's detail.
 *
 * The table went the way the routines table went, and for the same reason: the
 * rail is the list. What its columns carried moved to where each fact belongs —
 * readiness and last use onto the credential's own Overview, tags into its
 * header, agents onto its Used by tab, and the shape of the whole vault into
 * the cards here.
 *
 * Shares its shell components with /dashboard and /routines rather than
 * imitating them, so an amber arc means the same thing on all three.
 */

import * as React from "react"
import Link from "next/link"
import {
  AlertTriangle,
  CheckCircle2,
  Clock,
  KeyRound,
  Layers,
  ShieldCheck,
} from "lucide-react"

import { Appear, AppearStack } from "@/components/ui/detail"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { StatusDonut } from "@/components/features/dashboard/status-donut"
import { Skeleton } from "@/components/ui/skeleton"
import { CredentialTierBadge } from "./credential-tier-badge"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import { formatRelativeTime } from "@/lib/time"
import {
  attentionQueue,
  expiringSoon,
  recentlyUsed,
  typeBreakdown,
  vaultTotals,
  type OverviewCredential,
} from "@/lib/credentials/overview"
import { guardedCount, tierBuckets, tierOf } from "@/lib/credentials/tiers"
import { EXPIRY_WARNING_DAYS } from "@/lib/credentials/facets"
import { cn } from "@/lib/utils"

const ATTENTION_LIMIT = 6
const RECENT_LIMIT = 6
const TYPE_LIMIT = 7

export interface CredentialsOverviewCredential extends OverviewCredential {
  security_level_label?: string
}

export interface CredentialsOverviewProps {
  credentials: CredentialsOverviewCredential[]
  /** Credential ids a crew reported no CLI for. */
  missingToolIds: ReadonlySet<string>
  /** How many crews answered the readiness endpoint — 0 means "nobody asked yet". */
  crewsChecked: number
  readinessLoading: boolean
  onSelect: (id: string) => void
  /** Sets the rail's tier facet — the donut's click-through. */
  onSelectTier: (tier: string) => void
  /** Sets the rail's status facet — the attention card's click-through. */
  onSelectStatus: (status: "all" | "attention" | "missing-tool") => void
}

export function CredentialsOverview({
  credentials,
  missingToolIds,
  crewsChecked,
  readinessLoading,
  onSelect,
  onSelectTier,
  onSelectStatus,
}: CredentialsOverviewProps) {
  const totals = React.useMemo(() => vaultTotals(credentials), [credentials])
  const guarded = React.useMemo(() => guardedCount(credentials), [credentials])
  const buckets = React.useMemo(() => tierBuckets(credentials), [credentials])
  const types = React.useMemo(() => typeBreakdown(credentials), [credentials])
  const attention = React.useMemo(
    () => attentionQueue(credentials, missingToolIds, ATTENTION_LIMIT),
    [credentials, missingToolIds],
  )
  const expiring = React.useMemo(() => expiringSoon(credentials, RECENT_LIMIT), [credentials])
  const recent = React.useMemo(() => recentlyUsed(credentials, RECENT_LIMIT), [credentials])
  const missingToolCount = React.useMemo(
    () => credentials.filter((c) => missingToolIds.has(c.id)).length,
    [credentials, missingToolIds],
  )

  // Everything the attention queue could hold, not just the page of it shown —
  // a card headed "6" over six rows when nine are broken is a card lying by
  // omission.
  const attentionTotal = React.useMemo(
    () => attentionQueue(credentials, missingToolIds, Number.MAX_SAFE_INTEGER).length,
    [credentials, missingToolIds],
  )

  const shownTypes = types.slice(0, TYPE_LIMIT)
  const hiddenTypes = types.length - shownTypes.length

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4">
      <Appear order={0}>
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Overview</h1>
          <p className="text-xs text-muted-foreground">
            {totals.total} {totals.total === 1 ? "secret" : "secrets"} in this workspace
          </p>
        </div>
      </Appear>

      {/* ── What is usable, what is guarded, what is about to break ── */}
      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard
            label="Active"
            value={totals.active}
            valueColor={totals.active > 0 ? "rgb(52, 211, 153)" : undefined}
            subtitle={`of ${totals.total} total`}
          />
          {/* The tier tile, and it is a place to go. "How much of this vault
              does a model or a person have to clear?" is the question the tier
              column was added to answer, and it had no surface until now. */}
          <KpiCard
            label="Guarded · L3+"
            value={guarded}
            subtitle={
              guarded === 0
                ? "every secret is self-service"
                : `mediated per read · ${totals.total - guarded} self-service`
            }
            onClick={() => onSelectTier("3")}
          />
          <KpiCard
            label="Tools missing"
            value={missingToolCount}
            valueColor={missingToolCount > 0 ? "rgb(248, 113, 113)" : undefined}
            subtitle={
              readinessLoading
                ? "checking crews…"
                : crewsChecked === 0
                  ? "no crew reported"
                  : `across ${crewsChecked} crew${crewsChecked === 1 ? "" : "s"}`
            }
            onClick={missingToolCount > 0 ? () => onSelectStatus("missing-tool") : undefined}
          />
          <KpiCard
            label="Expiring"
            value={totals.expiring}
            valueColor={totals.expiring > 0 ? "rgb(251, 191, 36)" : undefined}
            subtitle="next 30 days"
          />
        </div>
      </Appear>

      {/* ── The vault by blast radius, and what needs a hand ── */}
      <Appear order={2}>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <DashboardCard
            title="Security tiers"
            icon={ShieldCheck}
            hint={`${totals.total} total`}
          >
            {/* The arcs sum to the vault, so the number in the centre is the
                number in the header. Clicking a slice narrows the rail to that
                tier — the donut is the tier filter's front door. */}
            <StatusDonut data={buckets} centerLabel="secrets" onSelect={onSelectTier} />
          </DashboardCard>

          <DashboardCard
            title="Needs attention"
            icon={AlertTriangle}
            hint={attentionTotal > 0 ? `${attentionTotal}` : "all clear"}
            action={
              attentionTotal > 0 ? (
                <button
                  type="button"
                  onClick={() => onSelectStatus("attention")}
                  className="text-primary hover:underline"
                >
                  {attentionTotal > attention.length ? "See all →" : "Review →"}
                </button>
              ) : undefined
            }
          >
            {attention.length === 0 ? (
              <Empty icon={CheckCircle2}>
                Nothing is expired, stale, or waiting on a decision.
              </Empty>
            ) : (
              <div className="flex flex-col">
                {attention.map((item) => {
                  const brand = getBrand(item.provider)
                  const Icon = brand.Icon
                  const body = (
                    <>
                      <Icon
                        className="h-4 w-4 shrink-0"
                        style={{ color: brandColor(brand) }}
                        aria-hidden="true"
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-foreground/90">
                        {item.name}
                      </span>
                      <span
                        className={cn(
                          "shrink-0 text-[10px]",
                          item.tone === "error" ? "text-destructive" : "text-warn",
                        )}
                      >
                        {item.reason}
                        {item.href && " →"}
                      </span>
                    </>
                  )
                  const rowClass =
                    "group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
                  // A row whose fix is somewhere else links there. Opening the
                  // credential to be told "approve it in the inbox" is the
                  // scavenger hunt the deep link exists to remove.
                  return item.href ? (
                    <Link key={item.id} href={item.href} className={rowClass}>
                      {body}
                    </Link>
                  ) : (
                    <button
                      key={item.id}
                      type="button"
                      onClick={() => onSelect(item.id)}
                      className={rowClass}
                    >
                      {body}
                    </button>
                  )
                })}
              </div>
            )}
          </DashboardCard>
        </div>
      </Appear>

      {/* ── What shape the vault is, and what it is doing ── */}
      <Appear order={3}>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <DashboardCard
            title="By type"
            icon={Layers}
            hint={`${types.length} ${types.length === 1 ? "type" : "types"}`}
          >
            {types.length === 0 ? (
              <Empty icon={Layers}>Nothing to break down yet.</Empty>
            ) : (
              <div className="flex flex-col gap-2">
                {shownTypes.map((row) => (
                  <div key={row.label} className="flex items-center gap-2.5">
                    <span className="w-[76px] shrink-0 truncate font-mono text-[11px] text-foreground/80">
                      {row.label}
                    </span>
                    {/* The bar is scaled against the vault, not against the
                        biggest row: "half of everything is an api key" is the
                        readable fact, and a chart normalised to its own maximum
                        always shows one full bar whatever the data says. */}
                    <span className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-white/[0.05]">
                      <span
                        className="block h-full rounded-full bg-primary/70"
                        style={{ width: `${Math.max(row.share * 100, 2)}%` }}
                      />
                    </span>
                    <span className="w-7 shrink-0 text-right font-mono text-[11px] tabular-nums text-muted-foreground">
                      {row.count}
                    </span>
                  </div>
                ))}
                {hiddenTypes > 0 && (
                  <p className="text-[10px] text-muted-foreground-soft">
                    +{hiddenTypes} more {hiddenTypes === 1 ? "type" : "types"}
                  </p>
                )}
              </div>
            )}
          </DashboardCard>

          <DashboardCard
            title={expiring.length > 0 ? "Expiring soon" : "Recently used"}
            icon={expiring.length > 0 ? Clock : KeyRound}
            hint={
              expiring.length > 0
                ? `next ${EXPIRY_WARNING_DAYS} days`
                : recent.length > 0
                  ? `last ${recent.length}`
                  : "never used"
            }
          >
            {/* One slot, two questions, and the urgent one wins it. A vault with
                nothing expiring does not need a card saying so; a vault with
                something expiring needs that above everything else. */}
            {expiring.length > 0 ? (
              <div className="flex flex-col">
                {expiring.map(({ credential, days }) => {
                  const brand = getBrand(credential.provider)
                  const Icon = brand.Icon
                  return (
                    <button
                      key={credential.id}
                      type="button"
                      onClick={() => onSelect(credential.id)}
                      className="group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
                    >
                      <span className="w-[52px] shrink-0 font-mono text-[11px] tabular-nums text-warn">
                        {days === 0 ? "today" : `${days}d`}
                      </span>
                      <Icon
                        className="h-4 w-4 shrink-0"
                        style={{ color: brandColor(brand) }}
                        aria-hidden="true"
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-foreground/90">
                        {credential.name}
                      </span>
                      <CredentialTierBadge
                        level={tierOf(credential)}
                        serverLabel={credential.security_level_label}
                      />
                    </button>
                  )
                })}
              </div>
            ) : recent.length === 0 ? (
              <Empty icon={KeyRound}>
                No agent has read a secret yet. Link one to a crew and run something.
              </Empty>
            ) : (
              <div className="flex flex-col">
                {recent.map((credential) => {
                  const brand = getBrand(credential.provider)
                  const Icon = brand.Icon
                  return (
                    <button
                      key={credential.id}
                      type="button"
                      onClick={() => onSelect(credential.id)}
                      className="group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
                    >
                      <Icon
                        className="h-4 w-4 shrink-0"
                        style={{ color: brandColor(brand) }}
                        aria-hidden="true"
                      />
                      <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-foreground/90">
                        {credential.name}
                      </span>
                      <CredentialTierBadge
                        level={tierOf(credential)}
                        serverLabel={credential.security_level_label}
                      />
                      <span className="shrink-0 text-[10px] text-muted-foreground-soft">
                        {formatRelativeTime(credential.last_used_at!)}
                      </span>
                    </button>
                  )
                })}
              </div>
            )}
          </DashboardCard>
        </div>
      </Appear>
    </div>
  )
}

function Empty({ icon: Icon, children }: { icon: typeof AlertTriangle; children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-1.5 py-7 text-center">
      <Icon className="h-4 w-4 text-muted-foreground-soft" />
      <p className="max-w-[280px] text-[11px] text-muted-foreground-soft">{children}</p>
    </div>
  )
}

/**
 * Skeleton in the final geometry.
 *
 * Placeholders that do not match what replaces them make the page reflow on
 * load, which reads as a second, unexplained render.
 */
export function CredentialsOverviewSkeleton() {
  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4">
      <Skeleton className="h-9 w-48" />
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <AppearStack>
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-[104px] rounded-xl" />
          ))}
        </AppearStack>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Skeleton className="h-[228px] rounded-xl" />
        <Skeleton className="h-[228px] rounded-xl" />
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Skeleton className="h-[228px] rounded-xl" />
        <Skeleton className="h-[228px] rounded-xl" />
      </div>
    </div>
  )
}
