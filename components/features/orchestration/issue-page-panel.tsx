"use client"

// The Issues panel on /issues/<id> (v67 I-FULL is drawn inside Issues with
// its panel, issues.js:182-187; PRD PNL-06). Same panel as /issues, but a
// navigator: a view, project, facet or search opens /issues with it. No row
// is selected — the page is an issue, not a view.

import { useCallback, useEffect, useMemo, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { parseSavedViews, issueViews } from "@/lib/saved-views"
import { SecondaryPanel } from "@/components/layout/secondary-panel"
import { NO_FILTERS, type IssueFilters } from "@/hooks/use-issue-filters"
import { useIssuesList } from "@/hooks/use-issues-list"
import { QUICK_VIEWS, applyQuickView, type QuickView } from "@/components/features/issues/issues-view-model"
import type { Mission, Project, SavedView } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"
import { IssuesPanel } from "./issues-panel"
import { filtersHref, panelHref, searchHref } from "./issues-page-links"

export interface IssuePageLists {
  issues: Mission[]
  projects: Project[]
  savedViews: SavedView[]
  crews: CrewSummary[]
}

/** What the panel and ↑/↓ read on the issue page: the first page of issues and the workspace lists. */
export function useIssuePageLists(workspaceId: string | null | undefined): IssuePageLists {
  const { issues } = useIssuesList(workspaceId)
  const [projects, setProjects] = useState<Project[]>([])
  const [savedViews, setSavedViews] = useState<SavedView[]>([])
  const [crews, setCrews] = useState<CrewSummary[]>([])
  useEffect(() => {
    if (!workspaceId) return
    let live = true
    const ws = encodeURIComponent(workspaceId)
    const get = (path: string) =>
      apiFetch(`/api/v1/${path}?workspace_id=${ws}`)
        .then((r) => (r.ok ? r.json() : null))
        .catch(() => null)
    void Promise.all([get("projects"), get("saved-views"), get("crews")]).then(([p, v, c]) => {
      if (!live) return
      if (Array.isArray(p)) setProjects(p)
      if (v) setSavedViews(issueViews(parseSavedViews(v)))
      if (Array.isArray(c)) setCrews(c)
    })
    return () => {
      live = false
    }
  }, [workspaceId])
  return { issues, projects, savedViews, crews }
}

export function IssuePagePanel({
  lists,
  me,
  navigate,
}: {
  lists: IssuePageLists
  me: string | null
  /** Leaves the page (the host asks first while edits wait). */
  navigate: (href: string) => void
}) {
  const [search, setSearch] = useState("")
  const quickCounts = useMemo(
    () => Object.fromEntries(QUICK_VIEWS.map((v) => [v.id, applyQuickView(lists.issues, v.id, me).length])) as Record<QuickView, number>,
    [lists.issues, me],
  )
  const onFilters = useCallback((patch: Partial<IssueFilters>) => navigate(filtersHref({ ...NO_FILTERS, ...patch })), [navigate])

  return (
    <SecondaryPanel id="issues" label="Issues views">
      <div
        className="contents"
        onKeyDown={(e) => {
          const t = e.target as HTMLElement
          if (e.key === "Enter" && t.closest("[data-issues-search]") && search.trim()) {
            e.preventDefault()
            navigate(searchHref(search))
          }
        }}
      >
        <IssuesPanel
          issues={lists.issues}
          quickCounts={quickCounts}
          projects={lists.projects}
          savedViews={lists.savedViews}
          crews={lists.crews}
          search={search}
          onSearchChange={setSearch}
          filters={NO_FILTERS}
          onFilters={onFilters}
          onClearFilters={() => {}}
          selection={null}
          onSelect={(s) => navigate(panelHref(s))}
          onNewProject={() => navigate("/issues?create=project")}
        />
      </div>
    </SecondaryPanel>
  )
}
