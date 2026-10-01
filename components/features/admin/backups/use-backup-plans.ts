"use client"

import { calendarFixture, plansFixture } from "./__fixtures__/backups"
import type { BackupPlan, CalendarResponse, CategoryKey, NextRunsResponse, PreviewContentsResponse, Preset } from "./backups-model"
import { INSTANCE_BACKUPS, listOf, send, useResource, type SendResult } from "./use-backups-data"

/** GET /admin/instance/backups/plans */
export function useBackupPlans() {
  return useResource<BackupPlan[]>(`${INSTANCE_BACKUPS}/plans`, plansFixture, listOf)
}

/** GET …/plans/{id}/next?n=5 — null id (a new plan) skips it. */
export function usePlanNext(id: string | null) {
  return useResource<NextRunsResponse>(id ? `${INSTANCE_BACKUPS}/plans/${encodeURIComponent(id)}/next?n=5` : null, () => ({ runs: [] }))
}

/** GET …/plans/{id}/calendar?from=&to= */
export function usePlanCalendar(id: string | null, from: string, to: string) {
  return useResource<CalendarResponse>(
    id ? `${INSTANCE_BACKUPS}/plans/${encodeURIComponent(id)}/calendar?from=${from}&to=${to}` : null,
    () => calendarFixture(new Date()),
  )
}

/** POST …/plans/preview-contents — the server's word on dependencies. */
export function previewContents(preset: Preset, contents: CategoryKey[], envMode: "files" | "complete"): Promise<SendResult<PreviewContentsResponse>> {
  return send(`${INSTANCE_BACKUPS}/plans/preview-contents`, "POST", { preset, contents, env_mode: envMode })
}

/** POST a new plan or PUT an existing one. */
export function savePlan(plan: Omit<BackupPlan, "id" | "next_run_at" | "last_run_at"> & { id?: string }): Promise<SendResult<BackupPlan>> {
  const { id, ...body } = plan
  return id
    ? send(`${INSTANCE_BACKUPS}/plans/${encodeURIComponent(id)}`, "PUT", body)
    : send(`${INSTANCE_BACKUPS}/plans`, "POST", body)
}

export function deletePlan(id: string): Promise<SendResult<unknown>> {
  return send(`${INSTANCE_BACKUPS}/plans/${encodeURIComponent(id)}`, "DELETE")
}
