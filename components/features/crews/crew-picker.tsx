"use client"

import { useMemo, useState } from "react"
import { Check, ChevronsUpDown, Users } from "lucide-react"

import { cn } from "@/lib/utils"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CREATE_SURFACE_INPUT } from "@/components/layout/create-surface"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"

/**
 * Pick a crew, with the face the crew wears everywhere else.
 *
 * This began as a local helper inside New routine, where it replaced a native
 * `<select>`. New agent still had that `<select>` — a flat alphabetical list
 * of every crew in the workspace, no icons, no colour, no search. On a
 * workspace with a few dozen crews (a dev box accumulates them fast) it is a
 * wall of near-identical strings, and the one column that would tell them
 * apart at a glance — the crew's own icon and colour — was the one the type
 * threw away three components up the chain.
 *
 * `agentCount` is optional and only drives the grouping: crews that already
 * have agents come first under their own heading, so the ones somebody is
 * actually working in are not interleaved with a hundred empty shells. When
 * no crew reports a count the list stays flat, because a heading that always
 * says the same thing is noise.
 */

export interface PickerCrew {
  id: string
  name: string
  slug?: string
  /** CrewIcon accepts a palette id or a hex, which is why neither is normalised. */
  icon?: string | null
  color?: string | null
  /** Drives grouping only. Absent on callers that do not have it. */
  agentCount?: number
}

export function CrewPicker({
  id,
  crews,
  value,
  onChange,
  placeholder,
  clearLabel,
  ariaLabel,
  className,
  /** Match on slug rather than id — New agent keys its draft by slug. */
  by = "id",
}: {
  id?: string
  crews: PickerCrew[]
  value: string
  onChange: (value: string) => void
  /** Shown when nothing is chosen — the old select's first <option>. */
  placeholder: string
  /** Present where "no crew" is a real answer, absent where it is not. */
  clearLabel?: string
  ariaLabel: string
  className?: string
  by?: "id" | "slug"
}) {
  const [open, setOpen] = useState(false)
  const keyOf = (c: PickerCrew) => (by === "slug" ? (c.slug ?? c.id) : c.id)
  const selected = crews.find((c) => keyOf(c) === value) ?? null

  // Grouped only when the caller supplied counts AND the split is real: all
  // populated or all empty is one group either way.
  const groups = useMemo(() => {
    const counted = crews.filter((c) => typeof c.agentCount === "number")
    if (counted.length !== crews.length) return null
    const staffed = crews.filter((c) => (c.agentCount ?? 0) > 0)
    const empty = crews.filter((c) => (c.agentCount ?? 0) === 0)
    if (staffed.length === 0 || empty.length === 0) return null
    return { staffed, empty }
  }, [crews])

  const row = (crew: PickerCrew) => (
    <CommandItem
      key={keyOf(crew)}
      // The id rides along so two crews sharing a name still filter and select
      // independently; it is not rendered, so the row's accessible name is
      // still just the crew's.
      value={`${crew.name} ${crew.id}`}
      onSelect={() => {
        onChange(keyOf(crew))
        setOpen(false)
      }}
    >
      <CrewIcon icon={crew.icon || "users"} color={crew.color} size="sm" className="!h-5 !w-5 !rounded" />
      <span className="truncate text-xs">{crew.name}</span>
      {typeof crew.agentCount === "number" && crew.agentCount > 0 && (
        <span className="ml-auto shrink-0 text-[10px] tabular-nums text-muted-foreground">
          {crew.agentCount}
        </span>
      )}
      {value === keyOf(crew) && <Check className="ml-1 h-3.5 w-3.5 shrink-0" />}
    </CommandItem>
  )

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          id={id}
          type="button"
          role="combobox"
          aria-expanded={open}
          aria-label={ariaLabel}
          className={cn(
            "flex w-full items-center gap-2 rounded-md border border-hairline bg-background px-2 text-left text-foreground outline-none transition-colors hover:border-border focus:border-primary",
            CREATE_SURFACE_INPUT,
            className,
          )}
        >
          {selected ? (
            <CrewIcon
              icon={selected.icon || "users"}
              color={selected.color}
              size="sm"
              className="!h-5 !w-5 !rounded"
            />
          ) : (
            <Users className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
          )}
          <span className={cn("min-w-0 flex-1 truncate", !selected && "text-muted-foreground")}>
            {selected?.name ?? placeholder}
          </span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[260px] p-0" align="start">
        <Command>
          <CommandInput placeholder="Search crews…" className="h-8 text-xs" />
          <CommandList>
            <CommandEmpty>No crews found.</CommandEmpty>
            {clearLabel && (
              <CommandGroup>
                <CommandItem
                  value={clearLabel}
                  onSelect={() => {
                    onChange("")
                    setOpen(false)
                  }}
                >
                  <Users className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
                  <span className="truncate text-xs text-muted-foreground">{clearLabel}</span>
                  {value === "" && <Check className="ml-auto h-3.5 w-3.5" />}
                </CommandItem>
              </CommandGroup>
            )}
            {groups ? (
              <>
                <CommandGroup heading="With agents">{groups.staffed.map(row)}</CommandGroup>
                <CommandGroup heading="Empty">{groups.empty.map(row)}</CommandGroup>
              </>
            ) : (
              <CommandGroup>{crews.map(row)}</CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
