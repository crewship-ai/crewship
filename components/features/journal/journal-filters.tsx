"use client"

import { useCallback, useEffect, useState } from "react"
import { Search, X } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import {
  ENTRY_TYPE_GROUPS,
  JOURNAL_SEVERITIES,
  type JournalEntryType,
  type JournalSeverity,
} from "@/lib/types/journal"

/** All filter values owned by the page — lifted so URL / deeplink sync is
 *  trivial to add later without touching the filter UI. */
export interface JournalFilterValue {
  types: JournalEntryType[]
  severities: JournalSeverity[]
  crewId: string
  agentId: string
  timeRange: "1h" | "24h" | "7d" | "30d" | "all"
  search: string
}

export const DEFAULT_JOURNAL_FILTERS: JournalFilterValue = {
  types: [],
  severities: [],
  crewId: "",
  agentId: "",
  timeRange: "24h",
  search: "",
}

interface CrewOption {
  id: string
  name: string
}

interface AgentOption {
  id: string
  name: string
  crew_id?: string | null
}

interface JournalFiltersProps {
  workspaceId: string | null
  value: JournalFilterValue
  onChange: (next: JournalFilterValue) => void
}

/**
 * Right-rail filter panel. Owns the UI for the six filter dimensions
 * (types, severity, crew, agent, time, search) and the crew/agent fetch
 * logic. Everything else on the page re-renders from `value`.
 */
export function JournalFilters({ workspaceId, value, onChange }: JournalFiltersProps) {
  const [crews, setCrews] = useState<CrewOption[]>([])
  const [agents, setAgents] = useState<AgentOption[]>([])
  const [searchLocal, setSearchLocal] = useState(value.search)

  // Debounce search so keystrokes don't refetch; 300 ms mirrors the audit log.
  useEffect(() => {
    const t = setTimeout(() => {
      if (searchLocal !== value.search) onChange({ ...value, search: searchLocal })
    }, 300)
    return () => clearTimeout(t)
  }, [searchLocal, value, onChange])

  // Mirror external resets (e.g. "clear all") back into the input.
  useEffect(() => { setSearchLocal(value.search) }, [value.search])

  // When the workspace changes, crew/agent ids from the previous workspace
  // are meaningless — clear them so stale selections don't filter the list.
  // Clear local option caches too so no ghosts flash in the dropdowns.
  useEffect(() => {
    setCrews([])
    setAgents([])
    if (value.crewId || value.agentId) {
      onChange({ ...value, crewId: "", agentId: "" })
    }
    // Intentionally omit `value` / `onChange` — this should fire only when
    // the workspace itself changes, not on every filter edit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        if (!res.ok) return
        const json = await res.json()
        if (!cancelled && Array.isArray(json)) {
          setCrews(json.map((c: { id: string; name: string }) => ({ id: c.id, name: c.name })))
        }
      } catch {
        /* silently ignore — filter stays empty */
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    const url = value.crewId
      ? `/api/v1/agents?workspace_id=${workspaceId}&crew_id=${value.crewId}`
      : `/api/v1/agents?workspace_id=${workspaceId}`
    ;(async () => {
      try {
        const res = await fetch(url)
        if (!res.ok) return
        const json = await res.json()
        if (!cancelled && Array.isArray(json)) {
          setAgents(
            json.map((a: { id: string; name: string; crew?: { id?: string } }) => ({
              id: a.id,
              name: a.name,
              crew_id: a.crew?.id ?? null,
            })),
          )
        }
      } catch {
        /* ignore */
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId, value.crewId])

  const toggleType = useCallback((t: JournalEntryType) => {
    const next = value.types.includes(t) ? value.types.filter((x) => x !== t) : [...value.types, t]
    onChange({ ...value, types: next })
  }, [value, onChange])

  const toggleSeverity = useCallback((s: JournalSeverity) => {
    const next = value.severities.includes(s) ? value.severities.filter((x) => x !== s) : [...value.severities, s]
    onChange({ ...value, severities: next })
  }, [value, onChange])

  const clearAll = useCallback(() => {
    setSearchLocal("")
    onChange(DEFAULT_JOURNAL_FILTERS)
  }, [onChange])

  const activeCount =
    value.types.length +
    value.severities.length +
    (value.crewId ? 1 : 0) +
    (value.agentId ? 1 : 0) +
    (value.search ? 1 : 0) +
    (value.timeRange !== "24h" ? 1 : 0)

  return (
    <aside className="w-full lg:w-72 shrink-0 space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">Filters</h2>
        {activeCount > 0 && (
          <Button variant="ghost" size="sm" className="h-6 px-1.5 text-[11px]" onClick={clearAll}>
            <X className="h-3 w-3 mr-1" /> Clear
          </Button>
        )}
      </div>

      {/* Search — full-text via FTS5 over summary + payload (Phase I
          of unified-journal). Debounced 300 ms in the parent before
          firing the server query. */}
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
        <Input
          value={searchLocal}
          onChange={(e) => setSearchLocal(e.target.value)}
          placeholder="Search summary + payload…"
          className="h-8 pl-7 text-xs"
        />
        <p className="mt-1 text-[10px] text-muted-foreground/70">
          Searches across all entries server-side
        </p>
      </div>

      {/* Time range */}
      <FilterSection title="Time range">
        <Select value={value.timeRange} onValueChange={(v) => onChange({ ...value, timeRange: v as JournalFilterValue["timeRange"] })}>
          <SelectTrigger className="h-8 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="1h" className="text-xs">Last 1 hour</SelectItem>
            <SelectItem value="24h" className="text-xs">Last 24 hours</SelectItem>
            <SelectItem value="7d" className="text-xs">Last 7 days</SelectItem>
            <SelectItem value="30d" className="text-xs">Last 30 days</SelectItem>
            <SelectItem value="all" className="text-xs">All time</SelectItem>
          </SelectContent>
        </Select>
      </FilterSection>

      {/* Crew / agent */}
      <FilterSection title="Scope">
        <Select
          value={value.crewId || "__all__"}
          onValueChange={(v) => onChange({ ...value, crewId: v === "__all__" ? "" : v, agentId: "" })}
        >
          <SelectTrigger className="h-8 text-xs">
            <SelectValue placeholder="All crews" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all__" className="text-xs">All crews</SelectItem>
            {crews.map((c) => (
              <SelectItem key={c.id} value={c.id} className="text-xs">
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={value.agentId || "__all__"}
          onValueChange={(v) => onChange({ ...value, agentId: v === "__all__" ? "" : v })}
        >
          <SelectTrigger className="h-8 text-xs">
            <SelectValue placeholder={value.crewId ? "All agents in crew" : "All agents"} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="__all__" className="text-xs">
              {value.crewId ? "All agents in crew" : "All agents"}
            </SelectItem>
            {agents.map((a) => (
              <SelectItem key={a.id} value={a.id} className="text-xs">
                {a.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </FilterSection>

      {/* Severity */}
      <FilterSection title="Severity">
        <div className="flex flex-wrap gap-1">
          {JOURNAL_SEVERITIES.map((s) => (
            <TogglePill key={s} label={s} active={value.severities.includes(s)} onClick={() => toggleSeverity(s)} />
          ))}
        </div>
      </FilterSection>

      {/* Entry types grouped */}
      <FilterSection title="Entry types">
        <div className="space-y-3">
          {ENTRY_TYPE_GROUPS.map((group) => (
            <div key={group.label}>
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground/60 font-semibold mb-1">
                {group.label}
              </div>
              <div className="flex flex-wrap gap-1">
                {group.types.map((t) => (
                  <TogglePill
                    key={t}
                    label={t}
                    active={value.types.includes(t)}
                    onClick={() => toggleType(t)}
                    mono
                  />
                ))}
              </div>
            </div>
          ))}
        </div>
      </FilterSection>
    </aside>
  )
}

function FilterSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold">{title}</div>
      <div className="space-y-1.5">{children}</div>
    </div>
  )
}

function TogglePill({
  label,
  active,
  onClick,
  mono = false,
}: {
  label: string
  active: boolean
  onClick: () => void
  mono?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        mono ? "h-5 px-1.5 text-[10px] font-mono" : "h-6 px-2 text-[11px]",
        "rounded border transition-colors",
        active
          ? "bg-primary/20 text-primary border-primary/50"
          : "bg-transparent text-muted-foreground border-border/60 hover:text-foreground hover:border-border",
      )}
    >
      {label}
    </button>
  )
}
