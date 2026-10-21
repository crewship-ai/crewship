"use client"

import * as React from "react"

import { useWorkspace } from "@/hooks/use-workspace"
import type { BackupsSection } from "@/app/(dashboard)/admin/navigation"
import { parseScope, resolveSelection, writeScope, type BackupScope, type ScopeWorkspace } from "./backups-model"
import { useScopeWorkspaces } from "./use-backups-overview"
import { perform, useDemo } from "./use-backups-data"
import { runNow } from "./use-backup-runs"

/*
 * The scope every Backups page and Data retention share, and the context each
 * section gets. Its own module so that the pages which render sections
 * (BackupsConsole, BackupsPage, RetentionPage) and the sections themselves
 * import it from here rather than from each other (ADM-03).
 */

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
  /** A span picked on the Runs chart: only runs started in it. */
  runSlice?: { fromMs: number; toMs: number } | null
}

/** The panel toolbar's narrowing of runs: text, kind, least proof, last N days. */
export interface RunQuery {
  q: string
  kind: "" | "full" | "custom" | "environments"
  proof: "" | "1" | "2" | "3"
  period: "" | "1" | "7" | "30" | "90"
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
