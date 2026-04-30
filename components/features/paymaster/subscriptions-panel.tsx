"use client"

import { Sparkles } from "lucide-react"
import { formatRelativeTime } from "@/lib/time"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import type { SubscriptionUsageRow } from "@/lib/types/paymaster"

interface SubscriptionsPanelProps {
  rows: SubscriptionUsageRow[]
  loading: boolean
  /**
   * Surface fetch / parse failures explicitly rather than silently
   * collapsing to the empty state. Per CodeRabbit review: a
   * transport error or backend skew that produces zero rows is NOT the
   * same signal as "no subscription credentials are in use" — operators
   * looking at billing data need to know when the answer is "we don't
   * know" vs "there's nothing to know".
   */
  error?: string | null
  notConfigured?: boolean
}

/**
 * Subscription plans panel — surfaces flat-rate credentials (Anthropic Max,
 * Cursor Pro, ChatGPT+Codex, Google AI Pro, Copilot Pro+, Factory Droid)
 * alongside the metered $ tracking. Deliberately renders NO $ figure: the
 * subscription is a flat fee, the marginal token cost is structurally zero
 * from our perspective, and showing $0 (or fake $) misleads the operator.
 *
 * Pattern adapted from Helicone's "confidence labelling" practice — every
 * cost surface should tell the operator how trustworthy the number is. Here
 * the trust label is the most honest possible: "no per-call cost tracking,
 * flat-rate plan covers it".
 */
export function SubscriptionsPanel({
  rows,
  loading,
  error,
  notConfigured,
}: SubscriptionsPanelProps) {
  // Order matters: error → notConfigured → loading → empty → data.
  // Loading wins over data only when we have no rows yet so a refetch
  // doesn't clear an already-rendered list.
  const showError = !!error && !loading
  const showNotConfigured = !!notConfigured && !loading

  return (
    <Card className="py-3">
      <CardHeader className="px-4 pb-2">
        <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
          <Sparkles className="h-3 w-3 text-amber-400" />
          Subscription plans
          <span className="text-[10px] font-normal text-muted-foreground/60 normal-case tracking-normal">
            · flat-rate, no per-call $ tracking
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="px-3">
        {showError ? (
          <div className="rounded-md border border-red-500/40 bg-red-500/5 px-3 py-3 text-[11px] text-red-300">
            Couldn&apos;t load subscription usage ({error}). The list below is
            unavailable; metered totals above are unaffected.
          </div>
        ) : showNotConfigured ? (
          <div className="rounded-md border border-dashed border-border/60 bg-muted/20 px-3 py-3 text-[11px] text-muted-foreground">
            Subscription tracking endpoint isn&apos;t available on this
            backend yet. Upgrade crewshipd to surface flat-rate credential
            usage here.
          </div>
        ) : loading && rows.length === 0 ? (
          <div className="h-[120px] flex items-center justify-center text-[11px] text-muted-foreground">
            Loading…
          </div>
        ) : rows.length === 0 ? (
          <div className="rounded-md border border-dashed border-border/60 bg-muted/20 px-3 py-3 text-[11px] text-muted-foreground">
            No subscription credentials in use during this window. When agents
            run on Claude Code Max, Cursor Pro, Codex via ChatGPT, or other
            flat-rate plans, those calls show up here — without a misleading
            $ figure.
          </div>
        ) : (
          <ul className="divide-y divide-border/40">
            {rows.map((r) => (
              <li
                key={`${r.subscription_plan}::${r.provider}`}
                className="py-2 flex items-center gap-3"
              >
                <Badge
                  variant="outline"
                  className="bg-amber-500/10 text-amber-300 border-amber-500/30 text-[10px]"
                >
                  {r.subscription_plan}
                </Badge>
                <span className="text-[11px] font-mono text-muted-foreground/80">
                  {r.provider}
                </span>
                <span className="ml-auto text-[11px] text-foreground/80 tabular-nums">
                  {new Intl.NumberFormat().format(r.call_count)} calls
                </span>
                <span className="text-[11px] text-muted-foreground tabular-nums">
                  {new Intl.NumberFormat().format(
                    r.input_tokens + r.output_tokens,
                  )}{" "}
                  tok
                </span>
                <span className="text-[11px] text-muted-foreground tabular-nums w-24 text-right">
                  {r.last_ts ? formatRelativeTime(r.last_ts) : "—"}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
