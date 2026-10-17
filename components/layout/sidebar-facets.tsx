"use client"

import * as React from "react"
import { Check, ChevronDown, ChevronRight, Search, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"

/**
 * Inline facets for the secondary panel (Harbor 2, v67 `panel()` → `pk-fx`;
 * PRD PNL-02 and Q05).
 *
 * The Filter button in the panel toolbar opens a block of facets INSIDE the
 * panel, under the toolbar — never a floating popover — so two facets combine
 * in one visit and the list stays in view below. Each facet folds; its header
 * names the current choice ("Paused", "2 selected") and clears just that
 * facet. A facet lists at most six values and offers the rest through
 * "All N…", a searchable list of every value.
 *
 * Composition (the panel owns placement — directly under the toolbar and the
 * active chips, above spaces and views):
 *
 *   const facets = useSidebarFacets()
 *   <SidebarToolbar>
 *     <SidebarSearch … />
 *     <SidebarFilterButton activeCount={n} {...facets.triggerProps} />
 *   </SidebarToolbar>
 *   {!facets.open && <SidebarActiveChips>…"Status: Paused" chips…</SidebarActiveChips>}
 *   <SidebarFacetBlock {...facets.blockProps} label="Filter routines" activeCount={n} onClear={clearAll}>
 *     <SidebarInlineFacet label="Status" values={…} selected={…} onToggle={…} onClear={…} />
 *   </SidebarFacetBlock>
 *
 * Replaces `SidebarFilterPopover` in secondary panels; the popover stays for
 * surfaces without a panel until they move.
 */

/** How many values a facet lists before it offers "All N…" (PNL-02). */
export const FACET_VALUE_LIMIT = 6

export interface SidebarFacetValue {
  value: string
  label: React.ReactNode
  /** Items with this value over the whole space, not over what is on screen. */
  count?: number
  /** Plain text for the header summary and the All N… search when `label` is not a string. */
  text?: string
  /** A leading icon or avatar. */
  icon?: React.ReactNode
}

/**
 * Open state of a panel's facet block and the wiring between the Filter
 * button and the block it opens. Spread `triggerProps` on the
 * `SidebarFilterButton` and `blockProps` on the `SidebarFacetBlock`.
 */
export function useSidebarFacets({ defaultOpen = false }: { defaultOpen?: boolean } = {}) {
  const [open, setOpen] = React.useState(defaultOpen)
  const id = React.useId()
  const triggerRef = React.useRef<HTMLButtonElement | null>(null)
  const onClose = React.useCallback((returnFocus: boolean) => {
    setOpen(false)
    if (returnFocus) triggerRef.current?.focus()
  }, [])
  return {
    open,
    setOpen,
    triggerProps: {
      ref: triggerRef,
      "aria-expanded": open,
      "aria-controls": open ? id : undefined,
      onClick: () => setOpen((o) => !o),
      // Filter keeps focus after it opens the block, so the first Escape
      // comes from here; it closes the block the button controls (as the
      // popover this replaced did) and focus stays on the button. One Escape
      // closes one layer: the floating tablet panel around it stays open.
      onKeyDown: (e: React.KeyboardEvent<HTMLButtonElement>) => {
        if (e.key !== "Escape" || !open) return
        e.preventDefault()
        e.stopPropagation()
        setOpen(false)
      },
    },
    blockProps: { id, open, onClose },
  }
}

/** The block of facets under the panel toolbar. Renders nothing while closed. */
export function SidebarFacetBlock({
  id,
  open,
  onClose,
  label,
  activeCount = 0,
  onClear,
  className,
  children,
}: {
  id?: string
  open: boolean
  /** Escape inside the block (the trigger handles its own); `true` asks to return focus to the trigger. */
  onClose?: (returnFocus: boolean) => void
  /** Accessible name, e.g. "Filter routines". */
  label: string
  activeCount?: number
  /** Renders Clear in the header while something is set. */
  onClear?: () => void
  className?: string
  children: React.ReactNode
}) {
  if (!open) return null
  return (
    <div
      id={id}
      role="group"
      aria-label={label}
      data-slot="facet-block"
      onKeyDown={(e) => {
        // Only keys from the block itself: the All N… list is portalled, and
        // its Escape closes the list, not the block.
        if (e.key !== "Escape" || !onClose || !e.currentTarget.contains(e.target as Node)) return
        e.preventDefault()
        onClose(true)
      }}
      className={cn(
        "mx-2 mb-2 max-h-[60%] shrink-0 overflow-y-auto rounded-lg border border-border bg-card px-1 pb-1.5 pt-0.5",
        className,
      )}
    >
      <div className="flex items-center gap-2 px-2 pb-1 pt-1.5">
        <span className="text-micro font-semibold text-muted-foreground-soft">Filter</span>
        {activeCount > 0 && (
          <>
            <span data-testid="facet-block-count" className="font-mono text-micro tabular-nums text-muted-foreground-soft">
              {activeCount}
            </span>
            {onClear && (
              <button
                type="button"
                onClick={onClear}
                aria-label="Clear all filters"
                className="kit-tap ml-auto text-micro font-medium text-primary-hover hover:underline"
              >
                Clear
              </button>
            )}
          </>
        )}
      </div>
      {children}
    </div>
  )
}

function valueText(v: SidebarFacetValue): string {
  return v.text ?? (typeof v.label === "string" || typeof v.label === "number" ? String(v.label) : v.value)
}

/**
 * One facet: a folding header with the current choice and its clear, then at
 * most `limit` values and "All N…". Values toggle themselves and never touch
 * a neighbour; single- or multi-select is the consumer's `onToggle`.
 */
export function SidebarInlineFacet({
  label,
  values,
  selected,
  onToggle,
  onClear,
  limit = FACET_VALUE_LIMIT,
  defaultOpen,
  open: openProp,
  onOpenChange,
  allLabel,
  searchPlaceholder,
  emptyText = "Nothing matches.",
  className,
}: {
  label: string
  values: readonly SidebarFacetValue[]
  /** The chosen value(s); a single string for a single-choice facet. */
  selected: string | readonly string[] | null | undefined
  onToggle: (value: string) => void
  onClear: () => void
  limit?: number
  /** Folded by default unless the facet has a value. */
  defaultOpen?: boolean
  open?: boolean
  onOpenChange?: (open: boolean) => void
  /** Text of the full-list trigger; defaults to "All N…". */
  allLabel?: string
  /** Placeholder of the full-list search; defaults to "Find a <label>…". */
  searchPlaceholder?: string
  emptyText?: string
  className?: string
}) {
  const chosen = React.useMemo<readonly string[]>(
    () => (selected == null ? [] : typeof selected === "string" ? [selected] : selected),
    [selected],
  )
  const [uncontrolled, setUncontrolled] = React.useState(defaultOpen ?? chosen.length > 0)
  const open = openProp ?? uncontrolled
  const setOpen = (next: boolean) => {
    if (openProp === undefined) setUncontrolled(next)
    onOpenChange?.(next)
  }
  const bodyId = React.useId()
  const set = chosen.length > 0

  const summary = !set
    ? null
    : chosen.length > 1
      ? `${chosen.length} selected`
      : (() => {
          const v = values.find((x) => x.value === chosen[0])
          return v ? valueText(v) : chosen[0]
        })()

  // The first `limit` values, plus any chosen value past them, so a choice
  // made from All N… stays visible and removable here.
  const shown = values.slice(0, limit)
  for (const v of values.slice(limit)) if (chosen.includes(v.value)) shown.push(v)

  return (
    <div data-slot="facet" data-set={set || undefined} className={cn("mx-0.5", className)}>
      <div className="flex items-center rounded-md hover:bg-foreground/[0.04]">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={open ? bodyId : undefined}
          onClick={() => setOpen(!open)}
          className="kit-tap flex h-[var(--control-md)] min-w-0 flex-1 items-center gap-2 px-2 text-left text-body text-muted-foreground"
        >
          {open
            ? <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground-soft" aria-hidden />
            : <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground-soft" aria-hidden />}
          <span className={cn("shrink-0", set && "font-semibold text-foreground")}>{label}</span>
          {summary && (
            <span className="ml-auto min-w-0 max-w-[58%] truncate rounded-sm bg-primary/10 px-1.5 text-label text-primary-hover">
              {summary}
            </span>
          )}
        </button>
        {set && (
          <button
            type="button"
            onClick={onClear}
            aria-label={`Clear ${label}`}
            className="kit-tap mr-1 grid size-6 shrink-0 place-items-center rounded-md text-muted-foreground-soft hover:bg-surface-subtle hover:text-foreground"
          >
            <X className="h-3 w-3" aria-hidden />
          </button>
        )}
      </div>
      {open && (
        <div id={bodyId} role="group" aria-label={label} className="grid gap-px pb-2 pl-4">
          {shown.map((v) => (
            <FacetOption key={v.value} value={v} on={chosen.includes(v.value)} onToggle={() => onToggle(v.value)} />
          ))}
          {values.length > limit && (
            <FacetAll
              label={label}
              values={values}
              chosen={chosen}
              onToggle={onToggle}
              trigger={allLabel ?? `All ${values.length}…`}
              placeholder={searchPlaceholder ?? `Find a ${label.toLowerCase()}…`}
              emptyText={emptyText}
            />
          )}
        </div>
      )}
    </div>
  )
}

function FacetCheck({ on }: { on: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "grid size-3.5 shrink-0 place-items-center rounded-sm border",
        on ? "border-primary bg-primary text-primary-foreground" : "border-control-border bg-card",
      )}
    >
      {on && <Check className="h-2.5 w-2.5" />}
    </span>
  )
}

function FacetOption({ value, on, onToggle }: { value: SidebarFacetValue; on: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onToggle}
      className={cn(
        "kit-tap flex min-h-[var(--control-sm)] w-full min-w-0 items-center gap-2 rounded-md px-2 text-left text-control transition-colors hover:bg-foreground/[0.04] hover:text-foreground",
        on ? "font-medium text-foreground" : "text-muted-foreground",
      )}
    >
      <FacetCheck on={on} />
      {value.icon}
      <span className="min-w-0 flex-1 truncate">{value.label}</span>
      {value.count != null && (
        <span className="shrink-0 font-mono text-micro tabular-nums text-muted-foreground-soft">{value.count}</span>
      )}
    </button>
  )
}

/** "All N…": every value of the facet, searchable; picks toggle and keep it open. */
function FacetAll({
  label,
  values,
  chosen,
  onToggle,
  trigger,
  placeholder,
  emptyText,
}: {
  label: string
  values: readonly SidebarFacetValue[]
  chosen: readonly string[]
  onToggle: (value: string) => void
  trigger: string
  placeholder: string
  emptyText: string
}) {
  const [open, setOpen] = React.useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="kit-tap flex min-h-[var(--control-sm)] w-full min-w-0 items-center gap-2 rounded-md px-2 text-left text-control font-medium text-primary-hover hover:bg-foreground/[0.04]"
        >
          <Search className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 flex-1 truncate">{trigger}</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[min(320px,calc(100vw-24px))] p-0">
        <Command label={`All ${label}`}>
          <CommandInput placeholder={placeholder} className="h-9 text-control" />
          <CommandList className="max-h-[min(360px,55dvh)]">
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup>
              {values.map((v) => {
                const on = chosen.includes(v.value)
                return (
                  <CommandItem
                    key={v.value}
                    value={`${valueText(v)} ${v.value}`}
                    onSelect={() => onToggle(v.value)}
                    data-checked={on || undefined}
                    className="min-h-10 gap-2 coarse:min-h-[44px]"
                  >
                    <FacetCheck on={on} />
                    {v.icon}
                    <span className="min-w-0 flex-1 truncate text-control">{v.label}</span>
                    {v.count != null && (
                      <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{v.count}</span>
                    )}
                  </CommandItem>
                )
              })}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
