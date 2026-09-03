"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { apiFetch } from "@/lib/api-fetch"

/**
 * "Here is what has been built so far" — the left pane's running record of
 * everything the Crewship Guide has actually created in this workspace.
 *
 * Why this exists at all: the Guide can create three different kinds of
 * thing, and only ONE of them used to be visible. A crew arrives through the
 * proposal card, so the wizard knows about it; a routine and a page are
 * created by the agent calling save_routine / save_page inside its own
 * container, which the browser never hears about. A person who asked for
 * uptime monitoring watched the Guide announce a routine and a page in prose
 * and then saw an empty panel — the only proof those objects existed was the
 * agent's own word for it, which is exactly the thing the proposal card was
 * built to stop being the proof.
 *
 * So this polls the three list endpoints rather than trusting the transcript.
 * What it shows is what the workspace actually contains; if the Guide says it
 * made a page and the page is not here, the panel is right and the sentence
 * was wrong.
 *
 * The setup crew itself is filtered out on purpose — `_crewship-setup` is the
 * Guide's own machinery, not something the person asked for, and listing it
 * would make every fresh workspace look like it already had a crew.
 */

interface CreatedCrew {
  id: string
  slug: string
  name: string
  agentCount: number
}
interface CreatedRoutine {
  slug: string
  name: string
  status: string
}
interface CreatedPage {
  slug: string
  name: string
  panelCount: number
}

interface CreatedInventory {
  crews: CreatedCrew[]
  routines: CreatedRoutine[]
  pages: CreatedPage[]
}

const EMPTY: CreatedInventory = { crews: [], routines: [], pages: [] }

/** The Guide's own crew. Never shown — see the component doc comment. */
const SETUP_CREW_SLUG = "_crewship-setup"

function asArray(v: unknown): Record<string, unknown>[] {
  if (Array.isArray(v)) return v as Record<string, unknown>[]
  // Some list endpoints wrap in {items: [...]}; tolerate both rather than
  // rendering nothing because one of three shapes differed.
  if (v && typeof v === "object") {
    const items = (v as Record<string, unknown>).items
    if (Array.isArray(items)) return items as Record<string, unknown>[]
  }
  return []
}

function str(o: Record<string, unknown>, ...keys: string[]): string {
  for (const k of keys) {
    const v = o[k]
    if (typeof v === "string" && v) return v
  }
  return ""
}

function num(o: Record<string, unknown>, ...keys: string[]): number {
  for (const k of keys) {
    const v = o[k]
    if (typeof v === "number") return v
    if (Array.isArray(v)) return v.length
  }
  return 0
}

export function OnboardingCreatedPanel({
  workspaceId,
  onCrewsFound,
}: {
  workspaceId: string | null
  /** Called after every poll with the number of real (non-setup) crews the
   *  workspace holds. The wizard uses it to keep Launch reachable after a
   *  reload: the crews a Create click made are durable, but the page state
   *  that enabled Launch was not, and a person who refreshed after Create
   *  used to land on a disabled Launch with no way to finish setup. */
  onCrewsFound?: (count: number) => void
}) {
  const [inv, setInv] = useState<CreatedInventory>(EMPTY)
  // Poll while the Crew step is open. The agent creates these from inside its
  // container with no path back to this tab, so there is no event to listen
  // for — polling is the honest mechanism, not a shortcut around one.
  const stopped = useRef(false)

  const load = useCallback(async (ws: string) => {
    const q = `?workspace_id=${encodeURIComponent(ws)}`
    const [crewsRes, routinesRes, pagesRes] = await Promise.allSettled([
      apiFetch(`/api/v1/crews${q}`),
      apiFetch(`/api/v1/workspaces/${encodeURIComponent(ws)}/pipelines${q}`),
      apiFetch(`/api/v1/pages${q}`),
    ])

    const read = async (r: PromiseSettledResult<Response>) => {
      if (r.status !== "fulfilled" || !r.value.ok) return []
      return asArray(await r.value.json().catch(() => null))
    }

    const [crewRows, routineRows, pageRows] = await Promise.all([
      read(crewsRes),
      read(routinesRes),
      read(pagesRes),
    ])

    const crews = crewRows
        .filter((c) => str(c, "slug") !== SETUP_CREW_SLUG)
        .map((c) => ({
          id: str(c, "id"),
          slug: str(c, "slug"),
          name: str(c, "name") || str(c, "slug"),
          agentCount: num(c, "agent_count", "agentCount", "agents"),
        }))
    onCrewsFound?.(crews.length)
    setInv({
      crews,
      routines: routineRows.map((p) => ({
        slug: str(p, "slug"),
        name: str(p, "name") || str(p, "slug"),
        status: str(p, "status"),
      })),
      pages: pageRows.map((p) => ({
        slug: str(p, "slug"),
        name: str(p, "name") || str(p, "slug"),
        panelCount: num(p, "panel_count", "panelCount"),
      })),
    })
  }, [onCrewsFound])

  useEffect(() => {
    if (!workspaceId) return
    stopped.current = false
    void load(workspaceId)
    const t = setInterval(() => {
      if (!stopped.current) void load(workspaceId)
    }, 4000)
    return () => {
      stopped.current = true
      clearInterval(t)
    }
  }, [workspaceId, load])

  const total = inv.crews.length + inv.routines.length + inv.pages.length
  if (total === 0) return null

  const Crews = CONCEPT_ICON.crews
  const Routines = CONCEPT_ICON.routines
  const Pages = CONCEPT_ICON.pages

  return (
    <div
      data-testid="onboarding-created-panel"
      className="space-y-2 rounded-xl border border-border bg-card/60 p-3"
    >
      <div className="text-[10px] uppercase tracking-[0.14em] text-muted-foreground">
        Built so far
      </div>

      {inv.crews.map((c) => (
        <Row
          key={`crew-${c.slug}`}
          testid="onboarding-created-crew"
          icon={<Crews className="h-3.5 w-3.5" />}
          kind="Crew"
          name={c.name}
          // "2 agents" says what a crew IS to someone who has never met the
          // word. A bare name does not.
          detail={c.agentCount > 0 ? `${c.agentCount} ${c.agentCount === 1 ? "agent" : "agents"}` : ""}
        />
      ))}

      {inv.routines.map((r) => (
        <Row
          key={`routine-${r.slug}`}
          testid="onboarding-created-routine"
          icon={<Routines className="h-3.5 w-3.5" />}
          kind="Routine"
          name={r.name}
          // A routine saved by an agent lands "proposed", not running, and
          // that difference is the whole question a person has about it.
          // Saying "runs automatically" for something awaiting approval would
          // be the panel telling the same lie the transcript might.
          detail={r.status === "active" ? "runs automatically" : "waiting for approval"}
        />
      ))}

      {inv.pages.map((p) => (
        <Row
          key={`page-${p.slug}`}
          testid="onboarding-created-page"
          icon={<Pages className="h-3.5 w-3.5" />}
          kind="Page"
          name={p.name}
          detail={p.panelCount > 0 ? `${p.panelCount} ${p.panelCount === 1 ? "panel" : "panels"}` : ""}
        />
      ))}
    </div>
  )
}

function Row({
  testid,
  icon,
  kind,
  name,
  detail,
}: {
  testid: string
  icon: React.ReactNode
  kind: string
  name: string
  detail?: string
}) {
  return (
    <div
      data-testid={testid}
      className="flex items-center gap-2 rounded-lg border border-border bg-card px-2.5 py-2 text-xs"
    >
      <span className="shrink-0 text-muted-foreground">{icon}</span>
      <span className="min-w-0 flex-1 truncate">
        <span className="font-medium">{name}</span>
        {/* The KIND is spelled out next to every row on purpose. "Routine" and
            "Page" are Crewship's words, not the user's, and a first-run person
            has met neither — a labelled row teaches the vocabulary in place,
            which a bare list of names cannot. */}
        <span className="ml-1.5 text-muted-foreground">{kind}</span>
      </span>
      {detail ? <span className="shrink-0 text-muted-foreground">{detail}</span> : null}
    </div>
  )
}
