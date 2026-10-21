"use client"

import { SlidersHorizontal } from "lucide-react"

import { cn } from "@/lib/utils"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Segmented } from "@/components/ui/segmented"
import { SubBarSecondary } from "@/components/layout/sub-bar"
import {
  GROUP_OPTIONS,
  ORDER_OPTIONS,
  ROW_FIELDS,
  type GroupBy,
  type OrderBy,
  type RowField,
  type RowFields,
} from "./issues-view-model"

/**
 * Display options (⇧V): how the list groups and orders, and which fields a
 * row carries. Personal — they follow the reader, not the workspace.
 */
export function IssuesDisplayOptions({
  open,
  onOpenChange,
  groupBy,
  onGroupBy,
  orderBy,
  onOrderBy,
  fields,
  onFields,
  board = false,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  groupBy: GroupBy
  onGroupBy: (g: GroupBy) => void
  orderBy: OrderBy
  onOrderBy: (o: OrderBy) => void
  fields: RowFields
  onFields: (f: RowFields) => void
  /** The board always groups by status; only the order applies. */
  board?: boolean
}) {
  const toggle = (k: RowField) => onFields({ ...fields, [k]: !fields[k] })
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>
        <SubBarSecondary icon={SlidersHorizontal} title="Display options · ⇧V" aria-keyshortcuts="Shift+V">
          <span className="max-md:sr-only">Display</span>
        </SubBarSecondary>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[330px] p-2" aria-label="Display options">
        <p className="px-2 pb-1 pt-1.5 text-label font-semibold text-muted-foreground-soft">Display options</p>
        {!board && (
          <div className="grid gap-1 px-1.5 py-1">
            <span className="text-label text-foreground">Group by</span>
            <Segmented ariaLabel="Group by" size="xs" fill options={GROUP_OPTIONS.map((o) => ({ value: o.id, label: o.label }))} value={groupBy} onChange={onGroupBy} />
          </div>
        )}
        <div className="grid gap-1 px-1.5 py-1">
          <span className="text-label text-foreground">Order</span>
          <Segmented ariaLabel="Order" size="xs" fill options={ORDER_OPTIONS.map((o) => ({ value: o.id, label: o.label }))} value={orderBy} onChange={onOrderBy} className="[&>button]:px-1" />
        </div>
        {!board && (
          <>
            <p className="px-2 pb-1 pt-2.5 text-label font-semibold text-muted-foreground-soft">Show on rows</p>
            <div className="flex flex-wrap gap-1.5 px-1.5 pb-2">
              {ROW_FIELDS.map((f) => (
                <button
                  key={f.id}
                  type="button"
                  aria-pressed={fields[f.id]}
                  onClick={() => toggle(f.id)}
                  className={cn(
                    "inline-flex h-7 items-center rounded-full border px-2.5 text-label transition-colors",
                    fields[f.id] ? "chip-active border-transparent" : "border-border text-muted-foreground hover:text-foreground",
                  )}
                >
                  {f.label}
                </button>
              ))}
            </div>
          </>
        )}
        <p className="border-t border-border px-2 pb-0.5 pt-2 text-label text-muted-foreground-soft">Personal to you</p>
      </PopoverContent>
    </Popover>
  )
}
