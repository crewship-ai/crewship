import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

/**
 * The foot of a paged list: how many rows are on screen and one button for
 * the next step. Pairs with `useShowMore` (client) or `usePagedList` (server).
 * A list that never exceeded one step draws nothing.
 */
export function ShowMore({ shown, total, nextCount, onMore, noun, paged, loading, className }: {
  shown: number
  total: number
  nextCount: number
  onMore: () => void
  /** Plural noun for the count: "people", "runs". */
  noun?: string
  paged?: boolean
  loading?: boolean
  className?: string
}) {
  const more = shown < total
  if (!more && !paged) return null
  return (
    <div
      data-slot="show-more"
      className={cn("flex flex-wrap items-center justify-between gap-2.5 border-t border-border px-4 py-2 text-label text-muted-foreground tabular-nums", className)}
    >
      <span aria-live="polite">
        {more ? `${shown.toLocaleString("en")} of ${total.toLocaleString("en")}${noun ? ` ${noun}` : ""} shown` : `All ${total.toLocaleString("en")} shown`}
      </span>
      {more && (
        <Button type="button" variant="outline" size="sm" onClick={onMore} disabled={loading}>
          {`Show ${nextCount.toLocaleString("en")} more`}
        </Button>
      )}
    </div>
  )
}
