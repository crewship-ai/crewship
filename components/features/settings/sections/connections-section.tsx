"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { ArrowLeftRight, ArrowLeft, ArrowRight, Grid3x3, List, Minus } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { CrewIcon } from "@/components/ui/crew-icon"
import { crewColorHex } from "@/lib/entities"
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
  out: "Sends work",
  in: "Receives work",
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

/** A crew's colour as something inline styles can use. */
const tintOf = (crew?: Crew) => crewColorHex(crew?.color) ?? "currentColor"

// ── CrewLinkFlow ─────────────────────────────────────────────────────

/**
 * The link itself: a line between the two crews with an arrowhead per
 * permitted direction, lit in the colour of the crew the work flows toward.
 *
 * Drawn rather than only worded because direction is the whole content of a
 * link, and a column of dropdowns makes you read four labels to learn what one
 * glance could carry. It is `aria-hidden` — the control beside it still states
 * the state in words, so this adds nothing a screen reader has to parse.
 */
function CrewLinkFlow({ state, from, to }: { state: PairState; from: string; to: string }) {
  const out = state === "out" || state === "both"
  const back = state === "in" || state === "both"
  return (
    <span
      aria-hidden="true"
      data-link-state={state}
      style={{ ["--from-c" as string]: from, ["--to-c" as string]: to }}
      className="flex shrink-0 items-center gap-[3px]"
    >
      <span
        className={cn(
          "h-0 w-0 border-y-[3.5px] border-y-transparent border-r-[5px] transition-opacity",
          back ? "opacity-100" : "opacity-20",
        )}
        style={{ borderRightColor: back ? "var(--from-c)" : "currentColor" }}
      />
      <span className="relative h-[2px] w-8 rounded-full bg-border sm:w-11">
        <span
          className={cn(
            "absolute inset-0 rounded-full transition-opacity",
            state === "none" ? "opacity-0" : "opacity-100",
          )}
          style={{ backgroundImage: "linear-gradient(90deg, var(--from-c), var(--to-c))" }}
        />
      </span>
      <span
        className={cn(
          "h-0 w-0 border-y-[3.5px] border-y-transparent border-l-[5px] transition-opacity",
          out ? "opacity-100" : "opacity-20",
        )}
        style={{ borderLeftColor: out ? "var(--to-c)" : "currentColor" }}
      />
    </span>
  )
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
  // Two questions, two views: "what can THIS crew reach" (the editor) and
  // "show me every pair at once" (the audit). The matrix is read-only — one
  // writeable surface per object, and a grid is exactly where row-versus-column
  // gets misread on a directed edge.
  const [view, setView] = useState<"crew" | "matrix">("crew")

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
    // The re-point below is a delete followed by a create, and the create
    // can fail: a link the user asked to re-point would silently become no
    // link at all. Remember what was removed so every failure path — a
    // rejected POST or a thrown request — can put it back.
    let removed: Connection | null = null
    const restoreRemoved = async () => {
      if (!removed) return
      await apiFetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          from_crew_id: removed.from_crew_id,
          to_crew_id: removed.to_crew_id,
          direction: removed.direction,
        }),
      }).catch(() => {})
    }
    try {
      // "not linked" is the one state with no row, so it is a plain delete.
      //
      // It keeps its confirmation. Narrowing or re-pointing a link is
      // recoverable by picking another option; removing it severs every
      // dispatch, message and shared-file path between two crews, and the old
      // screen asked before doing that. Turning the control into a dropdown
      // made it a single arrow-key press, which is a reason to keep the
      // question, not to drop it.
      if (next === "none") {
        if (!existing) return
        if (!window.confirm(`Unlink ${selected.name} and ${other.name}? Agents will no longer be able to hand work between them.`)) {
          return
        }
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
        removed = existing
      }

      const res = await apiFetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ from_crew_id: from, to_crew_id: to, direction }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        await restoreRemoved()
        toast.error(body?.detail ?? body?.error ?? "Failed to change the link")
        // The rendered state is now a guess either way — re-read it.
        await fetchData()
        return
      }
      toast.success(`${selected.name} → ${other.name}: ${PAIR_LABELS[next].toLowerCase()}`)
      await fetchData()
    } catch {
      await restoreRemoved()
      toast.error("Failed to change the link")
      await fetchData()
    } finally {
      setPendingPair(null)
    }
  }, [selected, connectionFor, workspaceId, fetchData])

  if (loading) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-[300px] rounded-xl" />
      </div>
    )
  }

  if (crews.length < 2) {
    return (
      <SettingsCard title="Crew links" description="Which crews may hand work to which">
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
      actions={
        <div className="flex items-center rounded-md border border-border/60 p-0.5">
          {([
            ["crew", "Per crew", List],
            ["matrix", "Matrix", Grid3x3],
          ] as const).map(([key, label, Icon]) => (
            <button
              key={key}
              type="button"
              onClick={() => setView(key)}
              aria-pressed={view === key}
              aria-label={label}
              className={cn(
                "flex items-center gap-1.5 rounded px-2 py-1 text-[11px] font-medium transition-colors",
                view === key
                  ? "bg-accent text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              <Icon className="size-3" />
              <span className="hidden sm:inline">{label}</span>
            </button>
          ))}
        </div>
      }
    >
      {view === "matrix" ? (
        /* ── Audit view: every pair at once. Row hands work to column. ── */
        <div className="overflow-x-auto">
          <table
            className="w-full min-w-[420px] border-collapse text-xs"
            aria-label="Crew links, every pair"
          >
            <thead>
              <tr className="border-b border-border/60">
                <th
                  scope="col"
                  className="px-4 py-2 text-left text-[10px] font-semibold uppercase tracking-wider text-muted-foreground"
                >
                  Can hand work to →
                </th>
                {crews.map((c) => (
                  <th
                    key={c.id}
                    scope="col"
                    className="px-2 py-2 text-center text-[11px] font-medium text-muted-foreground"
                  >
                    <span className="block truncate">{c.name}</span>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {crews.map((row) => (
                <tr key={row.id} className="border-b border-border/40 last:border-b-0">
                  <th scope="row" className="px-4 py-2 text-left font-normal">
                    <button
                      type="button"
                      onClick={() => { setSelectedID(row.id); setView("crew") }}
                      aria-label={`Edit ${row.name}`}
                      className="flex items-center gap-2 rounded text-xs text-muted-foreground hover:text-foreground"
                    >
                      <CrewIcon icon={row.icon || "briefcase"} color={row.color} size="sm" />
                      <span className="truncate">{row.name}</span>
                    </button>
                  </th>
                  {crews.map((col) => {
                    if (col.id === row.id) {
                      return (
                        <td key={col.id} className="px-2 py-2 text-center text-muted-foreground/50">
                          —
                        </td>
                      )
                    }
                    const st = stateOf(connectionFor(row.id, col.id), row.id)
                    const can = st === "out" || st === "both"
                    const tint = tintOf(row)
                    return (
                      <td key={col.id} className="px-2 py-2 text-center">
                        <span
                          aria-label={`${row.name} ${can ? "can" : "cannot"} hand work to ${col.name}`}
                          className={cn(
                            "inline-grid h-6 w-7 place-items-center rounded font-mono text-xs",
                            can ? "font-medium" : "text-muted-foreground/40",
                          )}
                          style={
                            can
                              ? {
                                  color: tint,
                                  backgroundColor: `color-mix(in srgb, ${tint} 14%, transparent)`,
                                }
                              : undefined
                          }
                        >
                          {st === "both" ? "↔" : can ? "→" : "·"}
                        </span>
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        /* ── Editor: one crew's point of view. ── */
        <section aria-label="Crew links" className="flex flex-col sm:flex-row">
          {/* Pick the crew you are thinking about. Everything beside it is
              stated from ITS point of view, which is how the question arrives:
              "who can Engineering hand work to?" — not "list all edges".
              Below `sm` the rail lies down and scrolls sideways rather than
              eating a third of a phone screen. */}
          <div className="flex shrink-0 overflow-x-auto border-b border-border/40 sm:w-[172px] sm:flex-col sm:overflow-visible sm:border-b-0 sm:border-r lg:w-[196px]">
            {crews.map((c) => {
              const isSelected = c.id === selected?.id
              const n = linkCount(c.id)
              return (
                <button
                  key={c.id}
                  type="button"
                  onClick={() => setSelectedID(c.id)}
                  aria-pressed={isSelected}
                  // Name the crew, not "Engineering 2": the link count beside
                  // it is a glance aid, not part of what this button is.
                  aria-label={c.name}
                  className={cn(
                    "flex shrink-0 items-center gap-2 border-b-2 px-3 py-2 text-left transition-colors sm:w-full sm:border-b-0 sm:border-l-2",
                    isSelected ? "bg-accent/50" : "border-transparent hover:bg-white/[0.02]",
                  )}
                  style={isSelected ? { borderColor: tintOf(c) } : undefined}
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

          {/* One row per other crew, one control per row. The row wraps rather
              than squeezing: on a narrow pane the control drops to its own
              line instead of crushing the crew name to three letters. */}
          <div className="min-w-0 flex-1">
            {others.map((other, i) => {
              const state = stateOf(connectionFor(selected!.id, other.id), selected!.id)
              const StateIcon = PAIR_ICONS[state]
              const tint = state === "none" ? undefined : tintOf(state === "in" ? selected! : other)
              return (
                <div
                  key={other.id}
                  className={cn(
                    "flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5",
                    i < others.length - 1 && "border-b border-border/40",
                  )}
                >
                  <CrewLinkFlow state={state} from={tintOf(selected)} to={tintOf(other)} />
                  <CrewIcon icon={other.icon || "briefcase"} color={other.color} size="sm" />
                  <div className="min-w-0 flex-1 basis-32">
                    <div className="truncate text-xs text-foreground">{other.name}</div>
                    <div className="truncate text-[11px] text-muted-foreground">
                      {PAIR_HINTS[state]}
                    </div>
                  </div>

                  {canManage ? (
                    <Select
                      value={state}
                      disabled={pendingPair === other.id}
                      onValueChange={(v) => void applyState(other, v as PairState)}
                    >
                      <SelectTrigger
                        className={cn(
                          "h-7 w-[148px] rounded-full text-xs transition-colors",
                          state === "none" && "text-muted-foreground",
                        )}
                        aria-label={`Link between ${selected!.name} and ${other.name}`}
                        style={
                          tint
                            ? {
                                borderColor: `color-mix(in srgb, ${tint} 55%, transparent)`,
                                backgroundColor: `color-mix(in srgb, ${tint} 12%, transparent)`,
                              }
                            : undefined
                        }
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {(Object.keys(PAIR_LABELS) as PairState[]).map((s) => {
                          const Icon = PAIR_ICONS[s]
                          return (
                            <SelectItem key={s} value={s} className="text-xs">
                              <span className="flex items-center gap-2">
                                <Icon className="size-3 text-muted-foreground" />
                                {PAIR_LABELS[s]}
                              </span>
                            </SelectItem>
                          )
                        })}
                      </SelectContent>
                    </Select>
                  ) : (
                    <span
                      className="flex shrink-0 items-center gap-1.5 rounded-full border border-border/60 px-2.5 py-1 text-[11px] text-muted-foreground"
                      style={
                        tint ? { borderColor: `color-mix(in srgb, ${tint} 40%, transparent)` } : undefined
                      }
                    >
                      <StateIcon className="size-3" />
                      {PAIR_LABELS[state]}
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        </section>
      )}

      {!canManage && (
        <div className="border-t border-border/40 px-4 py-2 text-[11px] text-muted-foreground">
          Only managers and admins can change a link.
        </div>
      )}
    </SettingsCard>
  )
}
