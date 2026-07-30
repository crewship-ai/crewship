"use client"

import { useMemo, useState } from "react"
import { Check, Search, X } from "lucide-react"

import { cn } from "@/lib/utils"

import { ActorAvatar } from "./actor"
import type { Actor, ActorKind, SubjectFacet } from "./types"

// =============================================================================
// Subject picker — the answer to "what happens at a hundred agents".
//
// The first version listed every subject it could find and scrolled. Two
// things break at scale, and the second is the worse one:
//
//   1. Thirty names in a dropdown is not a filter, it is a second inbox.
//   2. The list was derived from the LOADED ROWS. With a LIMIT-100 window that
//      means an agent is offered only when they have been noisy — and the
//      reason you go looking for one is usually that they have gone quiet. The
//      name you want is missing precisely when you need it.
//
// So the picker searches the workspace roster, not the page. What it shows
// unprompted is the handful of subjects that actually have items right now,
// ranked by count, because that covers the common case in one click. Anything
// else is one keystroke away, grouped by kind so an agent and a routine that
// share a word do not interleave.
// =============================================================================

/** How many count-bearing subjects to show before asking the reader to type. */
const QUICK_LIMIT = 6
/** How many matches to render per group; more than this means "keep typing". */
const GROUP_LIMIT = 6

const KIND_LABEL: Record<ActorKind, string> = {
  agent: "Agents",
  routine: "Routines",
  system: "System",
  crew: "Crews",
  user: "People",
}

const KIND_ORDER: ActorKind[] = ["agent", "routine", "crew", "system", "user"]

export interface DirectoryEntry {
  id: string
  label: string
  kind: "agent" | "routine" | "system"
}

export interface SubjectPickerProps {
  /** Subjects present in the loaded rows, with their counts. */
  subjects: SubjectFacet[]
  /** Everything the workspace has — what typing searches. */
  directory: DirectoryEntry[]
  selected: string | null
  onChange: (id: string | null) => void
}

export function SubjectPicker({ subjects, directory, selected, onChange }: SubjectPickerProps) {
  const [query, setQuery] = useState("")
  const q = query.trim().toLowerCase()

  const countById = useMemo(
    () => new Map(subjects.map((s) => [s.id, s.count])),
    [subjects],
  )

  const quick = useMemo(
    () => [...subjects].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label)),
    [subjects],
  )

  const matches = useMemo(() => {
    if (!q) return []
    const hits = directory.filter((d) => d.label.toLowerCase().includes(q))
    const byKind = new Map<ActorKind, DirectoryEntry[]>()
    for (const hit of hits) {
      const list = byKind.get(hit.kind)
      if (list) list.push(hit)
      else byKind.set(hit.kind, [hit])
    }
    // Subjects that actually have items sort to the top of their group: a name
    // with a 3 next to it is nearly always the one being looked for.
    for (const list of byKind.values()) {
      list.sort((a, b) => (countById.get(b.id) ?? 0) - (countById.get(a.id) ?? 0) || a.label.localeCompare(b.label))
    }
    return KIND_ORDER
      .filter((k) => byKind.has(k))
      .map((kind) => ({ kind, entries: byKind.get(kind) ?? [] }))
  }, [q, directory, countById])

  const hiddenQuick = Math.max(0, quick.length - QUICK_LIMIT)
  const selectedLabel = selected ?? null

  return (
    <div>
      <div className="px-3 py-1 text-[9px] font-semibold uppercase tracking-wider text-foreground/40">
        Subject
      </div>

      <div className="px-2 pb-1.5">
        <div className="flex h-7 items-center gap-1.5 rounded-md border border-white/[0.08] bg-white/[0.04] px-2 focus-within:border-primary/40">
          <Search className="h-3 w-3 shrink-0 text-muted-foreground/50" />
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Find an agent or routine…"
            aria-label="Find a subject"
            data-testid="subject-search"
            className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground/40"
          />
          {query && (
            <button
              type="button"
              onClick={() => setQuery("")}
              aria-label="Clear subject search"
              className="shrink-0 text-muted-foreground/50 hover:text-foreground"
            >
              <X className="h-3 w-3" />
            </button>
          )}
        </div>
      </div>

      {selectedLabel && (
        <SubjectRow
          actor={{ kind: kindOf(selectedLabel, directory), id: selectedLabel, label: selectedLabel }}
          count={countById.get(selectedLabel)}
          active
          onClick={() => onChange(null)}
        />
      )}

      {!q && (
        <>
          {quick.slice(0, QUICK_LIMIT).map((s) => (
            s.id === selectedLabel ? null : (
              <SubjectRow
                key={s.id}
                actor={s}
                count={s.count}
                active={false}
                onClick={() => onChange(s.id)}
              />
            )
          ))}
          <p className="px-3 py-1.5 text-[10px] leading-snug text-muted-foreground-soft">
            {hiddenQuick > 0
              ? `${hiddenQuick} more with items, ${directory.length} in the workspace — type to find one.`
              : `${directory.length} agents and routines in the workspace — type to find one.`}
          </p>
        </>
      )}

      {q && matches.length === 0 && (
        <p className="px-3 py-2 text-[11px] text-muted-foreground-soft">No agent or routine matches “{query}”.</p>
      )}

      {q && matches.map(({ kind, entries }) => (
        <div key={kind}>
          <div className="px-3 pt-1 text-[9px] font-semibold uppercase tracking-wider text-foreground/30">
            {KIND_LABEL[kind]}
          </div>
          {entries.slice(0, GROUP_LIMIT).map((e) => (
            <SubjectRow
              key={e.id}
              actor={{ kind: e.kind, id: e.id, label: e.label, seed: e.kind === "agent" ? e.id : undefined }}
              count={countById.get(e.id)}
              active={selected === e.id}
              onClick={() => onChange(selected === e.id ? null : e.id)}
            />
          ))}
          {entries.length > GROUP_LIMIT && (
            <p className="px-3 py-1 text-[10px] text-muted-foreground-soft">
              +{entries.length - GROUP_LIMIT} more — keep typing.
            </p>
          )}
        </div>
      ))}
    </div>
  )
}

function kindOf(id: string, directory: DirectoryEntry[]): ActorKind {
  return directory.find((d) => d.id === id)?.kind ?? "agent"
}

function SubjectRow({
  actor, count, active, onClick,
}: {
  actor: Actor
  count?: number
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      data-testid={`subject-${actor.id}`}
      className={cn(
        "flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors hover:bg-white/[0.06]",
        active ? "text-primary" : "text-muted-foreground/80",
      )}
    >
      <ActorAvatar actor={actor} size={20} />
      <span className="min-w-0 flex-1 truncate">{actor.label}</span>
      {count != null && <span className="tabular-nums text-[10px] opacity-70">{count}</span>}
      {active && <Check className="h-3 w-3 shrink-0" />}
    </button>
  )
}
