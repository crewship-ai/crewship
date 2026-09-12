"use client"

/**
 * /work — the durable work ledger and the deliveries that produced it
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
 *
 * Two tabs, because they answer two different questions. "What happened to my
 * work" is the work ledger; "did you receive this one" is the delivery ledger,
 * and a delivery that was ignored has no work to be about. Joining them into
 * one list would have to invent a row for the ignored ones or hide them, and
 * hiding them is the thing the delivery ledger was built to stop.
 *
 * Layout keys on width, touch sizing on the pointer: the detail is a docked
 * panel from `md:` up and a full-height sheet below it, since a 420px panel on
 * a phone is the whole screen anyway — better to say so than to squeeze.
 */

import * as React from "react"
import { RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Skeleton } from "@/components/ui/skeleton"
import { TabBar } from "@/components/ui/tab-bar"
import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"
import {
  WORK_STATE_LABEL,
  WORK_STATES,
  useWorkItems,
  type WorkState,
} from "@/hooks/use-work-items"
import {
  useWebhookDeliveries,
  type DeliveryDecision,
} from "@/hooks/use-webhook-deliveries"
import { WorkItemsList } from "./work-items-list"
import { WebhookDeliveriesList } from "./webhook-deliveries-list"
import { WorkItemDetail } from "./work-item-detail"

type WorkTab = "work" | "deliveries"

export interface WorkLayoutProps {
  workspaceId: string
}

export function WorkLayout({ workspaceId }: WorkLayoutProps) {
  const [tab, setTab] = React.useState<WorkTab>("work")
  const [stateFilter, setStateFilter] = React.useState<WorkState | null>(null)
  const [decisionFilter, setDecisionFilter] = React.useState<DeliveryDecision | null>(null)
  const [selectedWorkId, setSelectedWorkId] = React.useState<string | null>(null)
  const isMobile = useIsMobile()

  const work = useWorkItems(workspaceId, { state: stateFilter })
  const deliveries = useWebhookDeliveries(workspaceId, { decision: decisionFilter })

  const openWork = React.useCallback((id: string) => {
    setTab("work")
    setSelectedWorkId(id)
  }, [])

  const refresh = tab === "work" ? work.refetch : deliveries.refetch
  const loading = tab === "work" ? work.loading : deliveries.loading
  const error = tab === "work" ? work.error : deliveries.error

  const detail = selectedWorkId ? (
    <WorkItemDetail
      workspaceId={workspaceId}
      workItemId={selectedWorkId}
      onNavigate={(id) => setSelectedWorkId(id)}
    />
  ) : null

  return (
    <div className="flex h-[calc(100dvh-48px)] min-h-0 flex-col">
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-2">
        <TabBar value={tab} onValueChange={(v) => setTab(v as WorkTab)} ariaLabel="Work ledger" layoutId="work-tabs">
          <TabBar.Item value="work" count={work.items.length}>Work</TabBar.Item>
          <TabBar.Item value="deliveries" count={deliveries.deliveries.length}>Deliveries</TabBar.Item>
        </TabBar>
        <Button
          variant="outline"
          size="sm"
          className="ml-auto h-7 px-2.5 text-xs coarse:h-12"
          onClick={() => void refresh()}
          disabled={loading}
        >
          <RefreshCw className={cn("h-3 w-3", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      <div className="shrink-0 overflow-x-auto border-b border-hairline px-2 py-1.5">
        {tab === "work" ? (
          <FilterRow
            options={[
              { value: null, label: "All" },
              ...WORK_STATES.map((s) => ({ value: s as string, label: WORK_STATE_LABEL[s] })),
            ]}
            value={stateFilter}
            onChange={(v) => setStateFilter(v as WorkState | null)}
            ariaLabel="Filter by state"
          />
        ) : (
          <FilterRow
            options={[
              { value: null, label: "All" },
              { value: "accepted", label: "Accepted" },
              { value: "ignored", label: "Ignored" },
            ]}
            value={decisionFilter}
            onChange={(v) => setDecisionFilter(v as DeliveryDecision | null)}
            ariaLabel="Filter by decision"
          />
        )}
      </div>

      {error && (
        <div className="shrink-0 border-b border-destructive/30 bg-destructive/5 px-3 py-2 text-[12px] text-destructive">
          {error.message}
        </div>
      )}

      <div className="flex min-h-0 flex-1">
        <div className="min-w-0 flex-1 overflow-y-auto">
          {loading ? (
            <div className="space-y-2 p-3">
              <Skeleton className="h-12 w-full" />
              <Skeleton className="h-12 w-full" />
              <Skeleton className="h-12 w-full" />
            </div>
          ) : tab === "work" ? (
            <WorkItemsList
              items={work.items}
              selectedId={selectedWorkId}
              onSelect={(item) => setSelectedWorkId(item.id)}
            />
          ) : (
            <WebhookDeliveriesList deliveries={deliveries.deliveries} onOpenWork={openWork} />
          )}
        </div>

        {!isMobile && selectedWorkId && (
          <aside
            aria-label="Work item detail"
            className="hidden w-[440px] shrink-0 overflow-y-auto border-l border-hairline md:block"
          >
            {detail}
          </aside>
        )}
      </div>

      {isMobile && (
        <Sheet open={Boolean(selectedWorkId)} onOpenChange={(open) => !open && setSelectedWorkId(null)}>
          <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
            <SheetHeader>
              <SheetTitle>Work item</SheetTitle>
            </SheetHeader>
            {detail}
          </SheetContent>
        </Sheet>
      )}
    </div>
  )
}

function FilterRow({
  options, value, onChange, ariaLabel,
}: {
  options: { value: string | null; label: string }[]
  value: string | null
  onChange: (value: string | null) => void
  ariaLabel: string
}) {
  return (
    <div role="group" aria-label={ariaLabel} className="flex items-center gap-1">
      {options.map((option) => {
        const active = option.value === value
        return (
          <button
            key={option.label}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(option.value)}
            className={cn(
              "shrink-0 rounded px-2 py-1 text-[11px] transition-colors coarse:min-h-12",
              active
                ? "bg-primary/15 text-primary-hover"
                : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
            )}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}
