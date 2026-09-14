"use client"

/**
 * The delivery ledger
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §5, §9).
 *
 * An IGNORED delivery is a first-class row here, not an absence. That is the
 * whole reason the ledger exists: before it, a filtered event or a provider
 * ping left no trace at all, so "we never got it" and "we got it and decided
 * not to act on it" were the same empty screen. The decision and its reason
 * are both shown.
 *
 * What is never shown: the raw body (the API does not return it) and the
 * signing secret (§9's credential rule — secrets appear at creation and never
 * in a list or a log).
 */

import * as React from "react"
import { CheckCircle2, Filter, Webhook } from "lucide-react"

import { EmptyState, Pill } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import { relTime } from "@/lib/time"
import type { WebhookDelivery } from "@/hooks/use-webhook-deliveries"

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B"
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export interface WebhookDeliveriesListProps {
  deliveries: readonly WebhookDelivery[]
  loading?: boolean
  selectedId?: string | null
  onSelect?: (delivery: WebhookDelivery) => void
  /** Opens the work item an accepted delivery produced. */
  onOpenWork?: (workId: string) => void
}

export function WebhookDeliveriesList({
  deliveries, loading = false, selectedId, onSelect, onOpenWork,
}: WebhookDeliveriesListProps) {
  if (!loading && deliveries.length === 0) {
    return (
      <EmptyState
        icon={Webhook}
        title="No deliveries recorded"
        description="Every inbound webhook is recorded here before anything is dispatched — including the ones a filter decided to ignore."
      />
    )
  }
  return (
    <div role="list" aria-label="Webhook deliveries" className="divide-y divide-hairline">
      {deliveries.map((delivery) => (
        <DeliveryRow
          key={delivery.id}
          delivery={delivery}
          selected={delivery.id === selectedId}
          onSelect={onSelect}
          onOpenWork={onOpenWork}
        />
      ))}
    </div>
  )
}

function DeliveryRow({
  delivery, selected, onSelect, onOpenWork,
}: {
  delivery: WebhookDelivery
  selected: boolean
  onSelect?: (delivery: WebhookDelivery) => void
  onOpenWork?: (workId: string) => void
}) {
  const accepted = delivery.filter_decision === "accepted"
  return (
    // The work chip is a SIBLING of the row button, not a child of it: an
    // interactive element inside a button is invalid, and it is also
    // unreachable by keyboard in every browser that tolerates the markup.
    <div
      role="listitem"
      className={cn("flex items-stretch gap-2 pr-3", selected && "bg-primary/[0.07]")}
    >
      <button
        type="button"
        onClick={() => onSelect?.(delivery)}
        aria-current={selected ? "true" : undefined}
        data-delivery-id={delivery.id}
        className={cn(
          "flex min-w-0 flex-1 flex-col gap-2 px-3 py-2.5 text-left transition-colors",
          "coarse:min-h-12 hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40",
          "md:grid md:grid-cols-[minmax(0,1.4fr)_minmax(0,1.4fr)_minmax(0,1fr)_auto] md:items-center md:gap-3",
        )}
      >
        <span className="min-w-0">
          <span className="block truncate text-[13px] text-foreground/90">
            {delivery.event_type || "unnamed event"}
            {delivery.event_action ? <span className="text-muted-foreground">.{delivery.event_action}</span> : null}
          </span>
          <span className="block truncate font-mono text-[10px] text-muted-foreground/70" title={delivery.source_delivery_id}>
            {delivery.source_delivery_id || delivery.id}
          </span>
        </span>

        <span className="min-w-0 text-[11px] text-muted-foreground">
          {/* The profile is RECORDED, not inferred — which signature profile
              actually verified this, in the ledger's own words. */}
          <span className="block truncate">
            {delivery.endpoint_kind || "endpoint"} · <span className="font-mono">{delivery.profile || "unverified"}</span>
          </span>
          <span className="block truncate text-muted-foreground/70">
            {formatBytes(delivery.body_bytes)} · {relTime(delivery.received_at)}
          </span>
        </span>

        <span className="min-w-0 text-[11px] text-muted-foreground">
          {delivery.filter_reason ? (
            <span className="block truncate" title={delivery.filter_reason}>{delivery.filter_reason}</span>
          ) : null}
          <span className="block truncate text-muted-foreground/70">
            {delivery.raw_body_available ? "payload retained" : "payload dropped — cannot be replayed"}
          </span>
        </span>

        <span className="flex shrink-0 items-center md:justify-end">
          <Pill tone={accepted ? "success" : "default"} data-testid={`delivery-decision-${delivery.id}`}>
            {accepted ? <CheckCircle2 className="h-3 w-3" /> : <Filter className="h-3 w-3" />}
            {accepted ? "Accepted" : "Ignored"}
          </Pill>
        </span>
      </button>

      {delivery.work_id ? (
        <button
          type="button"
          onClick={() => onOpenWork?.(delivery.work_id as string)}
          className="my-2 shrink-0 self-center rounded-full bg-primary/10 px-2.5 text-[10px] text-primary hover:bg-primary/20 coarse:min-h-12"
        >
          work {delivery.work_id.slice(0, 8)}…
        </button>
      ) : null}
    </div>
  )
}
