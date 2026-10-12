"use client"

import * as React from "react"

import { FIXTURE_WORKSPACES, overviewFixture } from "./__fixtures__/backups"
import { scopeQuery, type BackupScope, type OverviewResponse, type ScopeWorkspace } from "./backups-model"
import { INSTANCE_BACKUPS, listOf, useResource } from "./use-backups-data"

/**
 * Every workspace on the instance, for the scope bar. GET /admin/workspaces
 * without a workspace_id answers for the whole instance to an instance admin
 * (the same read Admin › People uses), and it is the cheapest list that has
 * slugs for the URL.
 */
export function useScopeWorkspaces() {
  return useResource<ScopeWorkspace[]>(
    "/api/v1/admin/workspaces",
    () => FIXTURE_WORKSPACES,
    (json) => listOf<{ id: string; name: string; slug: string; logo_url?: string | null; _count_crews?: number }>(json)
      .map((w) => ({ id: w.id, name: w.name, slug: w.slug, logoUrl: w.logo_url ?? null, crews: w._count_crews })),
  )
}

/** GET /admin/instance/backups/overview for the scope on screen. */
export function useBackupsOverview(scope: BackupScope, selected: Set<string>, all: ScopeWorkspace[]) {
  const q = scopeQuery(scope, selected, all)
  const ids = React.useMemo(() => [...selected].sort().join(","), [selected])
  return useResource<OverviewResponse>(
    `${INSTANCE_BACKUPS}/overview?${q}`,
    () => overviewFixture(new Date(), scope, ids ? ids.split(",") : []),
  )
}
