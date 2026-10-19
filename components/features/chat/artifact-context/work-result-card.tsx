"use client"
import { Button } from "@/components/ui/button"
import { deriveWorkCardView } from "./work-card-view"
import type { WorkCardActionId, WorkCardModel } from "./types"

interface WorkResultCardProps {
  card: WorkCardModel
  onAction: (action: WorkCardActionId, card: WorkCardModel, trigger: HTMLButtonElement) => void
}

/** Presentational card for one agent result. State comes from the server model only. */
export function WorkResultCard({ card, onAction }: WorkResultCardProps) {
  const view = deriveWorkCardView(card)
  const restricted = view.title === "Restricted item"
  return <article aria-label={view.title} data-run-state={card.runState} data-problem={card.problem ?? undefined}
    className="flex min-w-0 flex-col gap-2 rounded-xl border bg-card p-4">
    <div className="min-w-0">
      <p className="truncate text-sm font-medium">{view.title}</p>
      {!restricted && card.access.status === "readable" && <p className="mt-1 text-xs text-muted-foreground">{card.access.typeLabel} · {card.executor} · {card.requester === "you" ? "On behalf of you" : "On behalf of a human collaborator"}</p>}
    </div>
    <p role="status" className="text-sm font-medium">{view.headline}</p>
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 text-xs text-muted-foreground">
      <dt>Run</dt><dd>{view.runLabel}</dd>
      {!restricted && <><dt>Content</dt><dd>{view.contentLabel}</dd></>}
      {!restricted && <><dt>Freshness</dt><dd>{view.freshnessLabel}</dd></>}
    </dl>
    {view.detail && <p className="text-xs">{view.detail}</p>}
    {view.actions.length > 0 && <div className="flex flex-wrap gap-2">
      {view.actions.map(action => <Button key={action.id} type="button" size="sm" variant="outline" className="coarse:min-h-12"
        onClick={event => onAction(action.id, card, event.currentTarget)}>{action.label}</Button>)}
    </div>}
  </article>
}
