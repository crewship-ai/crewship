"use client"

import { useEffect, useMemo, useState } from "react"
import { Search, FileCode2, FileX2 } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CreateSurfaceLoading,
  CreateSurfaceNotice,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"
import { TriangleAlert } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import type { CrewTemplate } from "./api"
import { asCrewColor, type WizardState } from "./types"

/**
 * Who the crew starts with — one grid of tiles, the shape
 * docs/prd/create-surface-parity.md §6.4 specifies.
 *
 * It was a two-pane browser: a mode tab strip (Browse / Empty), a search row,
 * four source tabs (Built-in · Mine · Workspace · Marketplace-soon), a row of
 * category chips, a scrolling two-column result grid, and a 320px preview
 * pane beside it — five layers of chrome over twelve rows of data, none of
 * which the other eleven doors have. It also made "Empty crew" a TAB, so the
 * option to start with nobody was a different mode of the screen rather than
 * one of the things you can pick.
 *
 * What is gone with the chrome: the source tabs and category chips. Search
 * still matches name, description and category, each tile still says whether
 * it is built-in or this workspace's, and twelve rows do not need faceting.
 * What is NOT gone: every template the server returns, and the empty option.
 *
 * Above the grid sit the two ways in that are not a template — "Start empty"
 * and "Import YAML" — because neither is a point on the "which team is
 * closest" axis the templates live on. See the comment at the row itself for
 * why empty is an action up there rather than the loudest thing on screen.
 */

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
  /** The dialog's workspace. GET /api/v1/crew-templates is wsCtx-wrapped and
   *  400s without it — see the fetch below. */
  workspaceId: string
  /** Opens the wizard's YAML import panel. Not a step; see import-panel.tsx. */
  onImport: () => void
}

export function StepLineup({ state, setState, workspaceId, onImport }: Props) {
  const [templates, setTemplates] = useState<CrewTemplate[] | null>(null)
  const [query, setQuery] = useState("")
  const [loadError, setLoadError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    // The workspace goes in the query string, not because this list is
    // workspace-scoped data the client wants filtered, but because the route
    // is wrapped in RequireWorkspace: no workspace_id anywhere in query, path
    // or header and the request never reaches the handler at all.
    apiFetch(`/api/v1/crew-templates?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data: CrewTemplate[]) => {
        if (!cancelled) setTemplates(Array.isArray(data) ? data : [])
      })
      .catch((e) => {
        if (!cancelled) {
          setTemplates([])
          setLoadError(e instanceof Error ? e.message : String(e))
        }
      })
    return () => { cancelled = true }
  }, [workspaceId])

  const filtered = useMemo(() => {
    if (!templates) return []
    const q = query.trim().toLowerCase()
    if (!q) return templates
    return templates.filter((t) =>
      t.name.toLowerCase().includes(q) ||
      (t.description ?? "").toLowerCase().includes(q) ||
      t.category.toLowerCase().includes(q),
    )
  }, [templates, query])

  const picked = state.mode === "empty"
    ? null
    : filtered.find((t) => t.slug === state.pickedTemplateSlug) ?? null

  // Adopt a template's identity the first time one is picked, and only while
  // the identity step is still untouched — a name somebody typed is theirs.
  function choose(t: CrewTemplate) {
    setState({
      mode: "browse",
      pickedTemplateSlug: t.slug,
      pickedTemplateMeta: {
        name: t.name,
        agentCount: t.agents.length,
        agents: t.agents.map((a) => ({ name: a.name, agent_role: a.agent_role })),
      },
      ...(state.name.trim() === "" && state.slug.trim() === ""
        ? {
            name: t.name,
            slug: t.slug,
            description: state.description || t.description || "",
            icon: t.icon || state.icon,
            color: asCrewColor(t.color) || state.color,
          }
        : {}),
    })
  }

  // Keep the wizard's picked meta in step with what is on screen: a template
  // filtered out by a search still deployed if state kept pointing at it.
  //
  // `templates === null` is NOT "filtered out", and the guard is the whole
  // reason this effect is safe. Step 2 is conditionally rendered, so
  // Continue → Back remounts this component and restarts the fetch; until it
  // lands `filtered` is [], which read as "the picked template is not on
  // screen" and cleared a pick the user had already made. Continue then sat
  // disabled on a step they had completed, with nothing on screen saying why.
  useEffect(() => {
    if (templates === null) return
    if (state.mode === "empty") return
    if (state.pickedTemplateSlug && !filtered.some((t) => t.slug === state.pickedTemplateSlug)) {
      setState({ pickedTemplateSlug: null, pickedTemplateMeta: null })
    }
  }, [templates, filtered, state.mode, state.pickedTemplateSlug, setState])

  return (
    <div className="flex flex-col gap-3">
      {/* The two ways in that are not a template, lifted out of the grid.
       *
       * "Empty crew" was one tile among thirteen, which put "start with
       * nobody" behind the same scan as "which of these twelve teams is
       * closest". It is not that kind of choice — it is the other axis — so
       * it sits above them as an action, one click away without being
       * searched for.
       *
       * It stays an action rather than becoming the visually dominant
       * option, deliberately. These twelve templates are the only thing on
       * the whole surface that shows a first-time user what a crew IS — four
       * agents with roles, not one bot. Make "empty" the loudest thing here
       * and most people take it and then have no idea what to do with what
       * they got. */}
      <div className="grid gap-2 sm:grid-cols-2 group-data-[mobile=true]/surface:grid-cols-1">
        <CreateSurfaceTile
          icon={FileX2}
          accent="slate"
          title="Start empty"
          description="No agents to begin with. Hire into it whenever — from the roster, or crewship agent create --crew <slug>."
          meta="0 agents"
          selected={state.mode === "empty"}
          onClick={() => setState({ mode: "empty", pickedTemplateSlug: null, pickedTemplateMeta: null })}
        />
        <CreateSurfaceTile
          icon={FileCode2}
          accent="sky"
          title="Import YAML"
          description="Fill this form from a kind: Crew manifest — the same file crewship apply takes."
          meta="from a file"
          onClick={onImport}
        />
      </div>

      <div className="flex items-center gap-2 pt-1">
        <span className="text-[11px] uppercase tracking-wider text-muted-foreground-soft">
          Or start from a template
        </span>
        <span className="h-px flex-1 bg-hairline" aria-hidden="true" />
      </div>

      <div className="relative">
        <label htmlFor="crew-template-search" className="sr-only">Search crew templates</label>
        <Search
          className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground-soft"
          aria-hidden="true"
        />
        <input
          id="crew-template-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder='Search templates… (e.g. "saas", "research")'
          className="h-8 w-full rounded-md border border-hairline bg-background pl-8 pr-2 text-xs outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20 max-sm:h-12 max-sm:text-sm"
        />
      </div>

      {templates === null && <CreateSurfaceLoading rows={3} />}

      {loadError && (
        <CreateSurfaceNotice tone="error" icon={TriangleAlert}>
          The crew templates did not load ({loadError}). The list is served by{" "}
          <code className="font-mono">/api/v1/crew-templates</code>. You can still start an empty
          crew below.
        </CreateSurfaceNotice>
      )}

      {templates !== null && !loadError && filtered.length === 0 && (
        <p className="rounded-lg border border-dashed border-border/60 px-3 py-5 text-center text-xs text-muted-foreground">
          {query.trim() ? `Nothing matches “${query}”.` : "No crew templates in this workspace."}
        </p>
      )}

      <div className="grid gap-2 sm:grid-cols-2 group-data-[mobile=true]/surface:grid-cols-1">
        {filtered.map((t) => (
          <CreateSurfaceTile
            key={t.slug}
            // The crew's own icon and colour, not a concept glyph: these are
            // the faces the roster shows, and they are how you recognise a
            // template you have deployed before.
            leading={<CrewIcon icon={t.icon || "users"} color={t.color || "blue"} size="sm" />}
            title={t.name}
            description={t.description}
            meta={`${t.agents.length} ${t.agents.length === 1 ? "agent" : "agents"}${t.is_builtin ? "" : " · workspace"}`}
            selected={picked?.slug === t.slug}
            onClick={() => choose(t)}
          />
        ))}
      </div>
    </div>
  )
}
