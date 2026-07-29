"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { ArrowLeftRight, ArrowLeft, ArrowRight, Minus } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { CrewIcon } from "@/components/ui/crew-icon"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import { useAbilities } from "@/hooks/use-abilities"
import { isManagerTier } from "@/lib/permissions/tiers"
import { SettingsCard } from "../shared"

// ── Types ────────────────────────────────────────────────────────────

interface Crew {
  id: string
  name: string
  slug: string
  color?: string | null
  /** The icon picked in Crews & Agents. A crew must look the same wherever it
   *  appears, or the reader has to fall back to reading names. */
  icon?: string | null
}

interface Connection {
  id: string
  from_crew_id: string
  to_crew_id: string
  direction: string
  status: string
}

interface ConnectionsSectionProps {
  workspaceId: string
}

/**
 * The state of one pair, read from the SELECTED crew's side.
 *
 * The stored row has an orientation and the reader does not think in
 * orientations — they think "can Engineering hand work to Ops, and can Ops
 * hand work back". These four states are that question, and every one of them
 * is a real state the table can hold.
 */
type PairState = "none" | "out" | "in" | "both"

const PAIR_LABELS: Record<PairState, string> = {
  none: "Not linked",
  out: "Sends work →",
  in: "← Receives work",
  both: "Both ways",
}

const PAIR_ICONS: Record<PairState, typeof Minus> = {
  none: Minus,
  out: ArrowRight,
  in: ArrowLeft,
  both: ArrowLeftRight,
}

/** What each state means, in terms of what an agent can actually do. */
const PAIR_HINTS: Record<PairState, string> = {
  none: "Neither crew can reach the other",
  out: "This crew can hand work to them",
  in: "They can hand work to this crew",
  both: "Either crew can hand work to the other",
}

/** How the selected crew sees a stored row. */
function stateOf(conn: Connection | undefined, selfID: string): PairState {
  if (!conn) return "none"
  if (conn.direction === "bidirectional") return "both"
  return conn.from_crew_id === selfID ? "out" : "in"
}

// ── Component ────────────────────────────────────────────────────────

export function ConnectionsSection({ workspaceId }: ConnectionsSectionProps) {
  // Both `POST` and `DELETE /crew-connections` are `roleCreate` server-side
  // (MANAGER+, internal/api/router_crews.go). This component has no
  // existing role prop, so pull it from useAbilities() rather than adding
  // one — but gate on isManagerTier, not abilities.can(...): see
  // lib/permissions/tiers.ts for why CASL isn't the right check here.
  const { role: callerRole } = useAbilities()
  const canManage = isManagerTier(callerRole)

  const [crews, setCrews] = useState<Crew[]>([])
  const [connections, setConnections] = useState<Connection[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedID, setSelectedID] = useState<string | null>(null)
  const [pendingPair, setPendingPair] = useState<string | null>(null)

  const fetchData = useCallback(async () => {
    try {
      const [connsRes, crewsRes] = await Promise.all([
        apiFetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`),
        apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`),
      ])
      if (connsRes.ok) setConnections(await connsRes.json())
      if (crewsRes.ok) setCrews(await crewsRes.json())
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { fetchData() }, [fetchData])

  const selected = useMemo(
    () => crews.find((c) => c.id === selectedID) ?? crews[0],
    [crews, selectedID],
  )

  /** The stored row for a pair, in whichever orientation it was written. */
  const connectionFor = useCallback(
    (a: string, b: string) =>
      connections.find(
        (c) =>
          (c.from_crew_id === a && c.to_crew_id === b) ||
          (c.from_crew_id === b && c.to_crew_id === a),
      ),
    [connections],
  )

  /** How many other crews this crew can reach or be reached by. */
  const linkCount = useCallback(
    (id: string) => connections.filter((c) => c.from_crew_id === id || c.to_crew_id === id).length,
    [connections],
  )

  const applyState = useCallback(async (other: Crew, next: PairState) => {
    if (!selected) return
    const existing = connectionFor(selected.id, other.id)
    const current = stateOf(existing, selected.id)
    if (current === next) return

    setPendingPair(other.id)
    try {
      // "not linked" is the one state with no row, so it is a plain delete.
      if (next === "none") {
        if (!existing) return
        const res = await apiFetch(
          `/api/v1/crew-connections/${existing.id}?workspace_id=${workspaceId}`,
          { method: "DELETE" },
        )
        if (!res.ok) { toast.error("Failed to unlink"); return }
        toast.success(`${selected.name} and ${other.name} unlinked`)
        await fetchData()
        return
      }

      // Both ways is orientation-free: the server upserts whichever row
      // exists, in either orientation, and widens it.
      const from = next === "in" ? other.id : selected.id
      const to = next === "in" ? selected.id : other.id
      const direction = next === "both" ? "bidirectional" : "unidirectional"

      // A one-way link has to point the right way. If the stored row points
      // the other way (or is bidirectional), there is no update that narrows
      // it — POST would read as "link this way too" and widen it back — so
      // the pair is replaced.
      const needsReplace =
        existing !== undefined &&
        direction === "unidirectional" &&
        (existing.direction === "bidirectional" || existing.from_crew_id !== from)
      if (needsReplace && existing) {
        const del = await apiFetch(
          `/api/v1/crew-connections/${existing.id}?workspace_id=${workspaceId}`,
          { method: "DELETE" },
        )
        if (!del.ok) { toast.error("Failed to change the link"); return }
      }

      const res = await apiFetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ from_crew_id: from, to_crew_id: to, direction }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.detail ?? body?.error ?? "Failed to change the link")
        return
      }
      toast.success(`${selected.name} → ${other.name}: ${PAIR_LABELS[next].replace(/[←→]/g, "").trim()}`)
      await fetchData()
    } catch {
      toast.error("Failed to change the link")
    } finally {
      setPendingPair(null)
    }
  }, [selected, connectionFor, workspaceId, fetchData])

  if (loading) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-[280px] rounded-xl" />
      </div>
    )
  }

  if (crews.length < 2) {
    return (
      <SettingsCard
        title="Crew links"
        description="Which crews may hand work to which"
      >
        <div className="px-4 py-10 text-center text-[11px] text-muted-foreground">
          A link joins two crews. This workspace has {crews.length === 0 ? "none" : "one"}.
        </div>
      </SettingsCard>
    )
  }

  const others = crews.filter((c) => c.id !== selected?.id)

  return (
    <SettingsCard
      title="Crew links"
      description="Which crews may hand work to which — agents can only dispatch, message and share files across a link"
    >
      <section
        aria-label="Crew links"
        className="flex min-h-[260px] flex-col sm:flex-row"
      >
        {/* Pick the crew you are thinking about. Everything to the right is
            stated from ITS point of view, which is how the question arrives:
            "who can Engineering hand work to?" — not "list all edges". */}
        <div className="shrink-0 border-b border-border/40 sm:w-[180px] sm:border-b-0 sm:border-r">
          {crews.map((c) => {
            const isSelected = c.id === selected?.id
            const n = linkCount(c.id)
            return (
              <button
                key={c.id}
                type="button"
                onClick={() => setSelectedID(c.id)}
                aria-pressed={isSelected}
                // Name the crew, not "Engineering 2": the link count beside it
                // is a glance aid, not part of what this button is.
                aria-label={c.name}
                className={cn(
                  "flex w-full items-center gap-2 px-3 py-2 text-left transition-colors",
                  isSelected ? "bg-accent/60" : "hover:bg-white/[0.02]",
                )}
              >
                <CrewIcon icon={c.icon || "briefcase"} color={c.color} size="sm" />
                <span className="min-w-0 flex-1 truncate text-xs font-medium">{c.name}</span>
                <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
                  {n || ""}
                </span>
              </button>
            )
          })}
        </div>

        {/* One row per other crew, one control per row. */}
        <div className="min-w-0 flex-1">
          {others.map((other, i) => {
            const state = stateOf(connectionFor(selected!.id, other.id), selected!.id)
            const StateIcon = PAIR_ICONS[state]
            return (
              <div
                key={other.id}
                className={cn(
                  "flex items-center justify-between gap-3 px-4 py-2.5",
                  i < others.length - 1 && "border-b border-border/40",
                )}
              >
                <div className="flex min-w-0 items-center gap-2">
                  <CrewIcon icon={other.icon || "briefcase"} color={other.color} size="sm" />
                  <div className="min-w-0">
                    <div className="truncate text-xs text-foreground">{other.name}</div>
                    <div className="truncate text-[11px] text-muted-foreground">
                      {PAIR_HINTS[state]}
                    </div>
                  </div>
                </div>

                {canManage ? (
                  <Select
                    value={state}
                    disabled={pendingPair === other.id}
                    onValueChange={(v) => void applyState(other, v as PairState)}
                  >
                    <SelectTrigger
                      className="h-7 w-[170px] text-xs"
                      aria-label={`Link between ${selected!.name} and ${other.name}`}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {(Object.keys(PAIR_LABELS) as PairState[]).map((s) => (
                        <SelectItem key={s} value={s} className="text-xs">
                          {PAIR_LABELS[s]}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : (
                  <span className="flex shrink-0 items-center gap-1.5 text-[11px] text-muted-foreground">
                    <StateIcon className="size-3" />
                    {PAIR_LABELS[state]}
                  </span>
                )}
              </div>
            )
          })}
        </div>
      </section>

      {!canManage && (
        <div className="border-t border-border/40 px-4 py-2 text-[11px] text-muted-foreground">
          Only managers and admins can change a link.
        </div>
      )}
    </SettingsCard>
  )
}
