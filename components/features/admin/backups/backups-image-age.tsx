"use client"

import * as React from "react"

import { SettingsRow } from "@/components/features/settings/shared"
import { StatusPill } from "@/components/ui/status-pill"
import { apiFetch } from "@/lib/api-fetch"

// ADM-08. The restore checks (internal/backup/restore_checks.go) do not compare
// image age, and nothing reads the archive's own images. What the server can
// say is how many crews on THIS server run an older image than the current one
// (GET /crews/{id}/image-status, `crewship crew image-status`). It is shown as
// that, as of now, and never as "current" when a crew could not be read.
// Remaining for the backend: a restore check over the archive's images (#2990).

type Result = { total: number; behind: number; unread: number } | "unavailable" | null

const MAX_CREWS = 50

export function useCrewImageAge(workspaceId: string | null): Result {
  const [result, setResult] = React.useState<Result>(null)
  React.useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}&limit=500`)
        if (!res.ok) throw new Error()
        const rows: unknown = await res.json()
        const crews = (Array.isArray(rows) ? (rows as { id: string }[]) : []).slice(0, MAX_CREWS)
        const states = await Promise.all(
          crews.map(async (c) => {
            try {
              const r = await apiFetch(`/api/v1/crews/${encodeURIComponent(c.id)}/image-status?workspace_id=${encodeURIComponent(workspaceId)}`)
              if (!r.ok) return "unread" as const
              const s = (await r.json()) as { behind?: boolean }
              return s.behind ? ("behind" as const) : ("current" as const)
            } catch {
              return "unread" as const
            }
          }),
        )
        if (!cancelled) setResult({ total: crews.length, behind: states.filter((s) => s === "behind").length, unread: states.filter((s) => s === "unread").length })
      } catch {
        if (!cancelled) setResult("unavailable")
      }
    })()
    return () => { cancelled = true }
  }, [workspaceId])
  return result
}

const crews = (n: number) => `${n} ${n === 1 ? "crew" : "crews"}`

export function ImageAgeCheck({ workspaceId }: { workspaceId: string | null }) {
  const result = useCrewImageAge(workspaceId)
  if (result === null || result === "unavailable" || result.total === 0) return null
  const { total, behind, unread } = result
  let label: string
  let tone: "warn" | "success" | "muted"
  if (behind > 0) {
    label = `${behind} of ${crews(total)} run an older image`
    tone = "warn"
  } else if (unread > 0) {
    label = `Could not read the image of ${unread} of ${crews(total)}`
    tone = "muted"
  } else {
    label = `All ${crews(total)} run the current image`
    tone = "success"
  }
  return (
    <div data-testid="image-age-check">
      <SettingsRow
        label="Crew images"
        description={`${label}${behind > 0 && unread > 0 ? `; ${unread} could not be read` : ""} · on this server now. The archive's own images are not compared; refresh a crew's image from its page.`}
      >
        <StatusPill tone={tone} label={tone === "warn" ? "older" : tone === "success" ? "ok" : "unknown"} />
      </SettingsRow>
    </div>
  )
}
