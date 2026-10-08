"use client"

import * as React from "react"
import { Plus } from "lucide-react"

import { useWorkspace } from "@/hooks/use-workspace"
import type { BackupsSection } from "@/app/(dashboard)/admin/navigation"
import { SmallButton } from "./backups-kit"
import { BackupsScopeBar } from "./backups-scope-bar"
import { INSTANCE_ONLY, SECTION_TITLE, parseScope, resolveSelection, writeScope, type BackupScope, type ScopeWorkspace } from "./backups-model"
import { useScopeWorkspaces } from "./use-backups-overview"
import { perform, useDemo } from "./use-backups-data"
import { runNow } from "./use-backup-runs"
import { BackupsOverview } from "./backups-overview"
import { BackupsHistory } from "./backups-history"
import { BackupsSchedules } from "./backups-schedules"
import { BackupsStorage } from "./backups-storage"
import { BackupsRecovery } from "./backups-recovery"
import { BackupsKeys } from "./backups-keys"
import { DataRetention } from "./data-retention"

/** What every section gets: the scope on screen and a way to move. */
export interface SectionCtx {
  scope: BackupScope
  selected: Set<string>
  workspaces: ScopeWorkspace[]
  currentWorkspaceId: string | null
  demo: boolean
  go: (section: BackupsSection, opts?: { run?: string; path?: string }) => void
  /** A run the history should open (Needs attention › See which). */
  focusRun: string | null
  /** A bundle the recovery wizard should start from (History › Restore…). */
  focusPath: string | null
  backUpNow: (workspaceIds?: string[]) => void
  /** Bumped by the heading's "New plan" button. */
  newPlanSignal: number
  /** The plan the side panel picked (Schedules opens it); "new" starts one. */
  focusPlan?: string | null
  /** The Runs facet the side panel picked (Backup history filters by it). */
  runFilter?: RunFilter
  /** True on the nested page, where the side panel lists plans and facets. */
  inDrill?: boolean
  /** The Recovery tab the side panel picked. */
  recoveryView?: "new" | "history" | "drills"
  /** The side panel's search and Filter, applied to runs before the facet. */
  runQuery?: RunQuery
}

/** The panel toolbar's narrowing of runs: text, kind, least proof, last N days. */
export interface RunQuery {
  q: string
  kind: "" | "full" | "custom" | "environments"
  proof: "" | "1" | "2" | "3"
  period: "" | "1" | "7" | "30"
}

/** The Runs facets of the side panel. */
export type RunFilter = "all" | "failed" | "incomplete" | "manual" | "pinned"

/**
 * The scope (?scope=&ws=), the workspaces it picks from, and the actions every
 * section shares. The nested Backups page and Data retention both build their
 * SectionCtx from this.
 */
export function useBackupsScope(forceWorkspaces = false) {
  const { workspaceId } = useWorkspace()
  const demo = useDemo()
  const ws = useScopeWorkspaces()
  const workspaces = React.useMemo(() => ws.data ?? [], [ws.data])
  const [urlState, setUrlState] = React.useState(() => parseScope(typeof window === "undefined" ? "" : window.location.search))
  const selected = React.useMemo(() => resolveSelection(urlState, workspaces), [urlState, workspaces])
  const scope: BackupScope = forceWorkspaces ? "workspaces" : urlState.scope
  const commit = React.useCallback((nextScope: BackupScope, next: Set<string>) => {
    const search = writeScope(window.location.search, nextScope, next, workspaces)
    window.history.replaceState(window.history.state, "", `${window.location.pathname}${search}`)
    setUrlState(parseScope(search))
  }, [workspaces])
  const backUpNow = React.useCallback((ids?: string[]) => {
    const inst = scope === "instance" && !ids
    void perform(demo, () => runNow({ scope: inst ? "instance" : "workspaces", workspace_ids: inst ? undefined : ids ?? [...selected], preset: inst ? "complete" : "workspace" }),
      "Backup started · it appears in Backup history", "The backup could not start")
  }, [demo, scope, selected])
  return { workspaceId: workspaceId ?? null, demo, workspaces, urlState, selected, scope, commit, backUpNow }
}

/**
 * Admin › Backups (six pages) and Admin › Data retention: the scope strip, the
 * page heading with its actions, and the page. The Admin sidebar picks the
 * page; the scope lives in the URL (?scope=instance|workspaces&ws=slug,…) so a
 * link shows what its sender saw.
 */
export function BackupsConsole({ page, onNavigate }: {
  page: BackupsSection | "retention"
  onNavigate: (section: BackupsSection) => void
}) {
  const { workspaceId, demo, workspaces, urlState, selected, scope, commit, backUpNow } = useBackupsScope(page === "retention")
  // ?run= opens that run in Backup history: the link a backup incident's
  // inbox card carries (View failure).
  const [focus, setFocus] = React.useState<{ run: string | null; path: string | null }>(() => ({
    run: typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("run"),
    path: null,
  }))
  const [newPlanSignal, setNewPlanSignal] = React.useState(0)

  const toggle = (id: string) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    commit(urlState.scope, next)
  }

  const ctx: SectionCtx = {
    scope, selected, workspaces, currentWorkspaceId: workspaceId, demo,
    focusRun: focus.run, focusPath: focus.path, backUpNow, newPlanSignal,
    go: (section, opts) => {
      setFocus({ run: opts?.run ?? null, path: opts?.path ?? null })
      onNavigate(section)
    },
  }

  const head = SECTION_TITLE[page]
  const actions =
    page === "overview" || page === "history" ? (
      <>
        <SmallButton onClick={() => ctx.go("schedules")}>Set up automatic backups</SmallButton>
        <SmallButton primary onClick={() => backUpNow()}>Back up now</SmallButton>
      </>
    ) : page === "schedules" ? (
      <SmallButton primary onClick={() => setNewPlanSignal((x) => x + 1)}><Plus className="h-3.5 w-3.5" />New plan</SmallButton>
    ) : null

  const mode = page === "retention" ? "workspaces-only" : INSTANCE_ONLY.has(page) ? "instance-only" : "scoped"
  const n = workspaces.length

  return (
    <div data-slot="backups-console" data-page={page} className="text-control">
      <BackupsScopeBar mode={mode} scope={scope} onScope={(s) => commit(s, selected)} workspaces={workspaces} selected={selected} onToggle={toggle}
        instanceSummary={`${n} workspace${n === 1 ? "" : "s"}, users, instance settings, container environments`} />
      <div className="mx-auto max-w-5xl space-y-3 p-4 md:p-6">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
          <h2 className="text-sm font-semibold">{head.title}</h2>
          <span className="text-control text-muted-foreground">{head.sub}</span>
          {actions && <div className="flex flex-wrap items-center gap-1.5 sm:ml-auto">{actions}</div>}
        </div>
        {demo && <p className="font-mono text-micro uppercase tracking-wide text-muted-foreground-soft">Demo data · ?demo=1 · nothing is sent</p>}
        {page === "overview" && <BackupsOverview ctx={ctx} />}
        {page === "history" && <BackupsHistory ctx={ctx} />}
        {page === "schedules" && <BackupsSchedules ctx={ctx} />}
        {page === "storage" && <BackupsStorage ctx={ctx} />}
        {page === "recovery" && <BackupsRecovery ctx={ctx} />}
        {page === "keys" && <BackupsKeys ctx={ctx} />}
        {page === "retention" && <DataRetention ctx={ctx} />}
      </div>
    </div>
  )
}
