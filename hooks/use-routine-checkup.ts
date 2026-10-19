"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type { Check } from "@/lib/routine-health"

interface ServerCheck {
  key: string
  level: "ok" | "warn" | "bad"
  title: string
  detail: string
  tab?: string
}

const FIX_LABEL: Record<string, string> = { overview: "Review", steps: "Open steps", triggers: "Open triggers", settings: "Open settings", versions: "Open versions" }

/** The server's checks for a routine (GET .../pipelines/{slug}/checkup,
 *  ODCH-RT-06), as the Checkup card draws them; null until it answers or when
 *  it cannot, and the card then shows what the page can prove itself. */
export function useRoutineCheckup(workspaceId: string | undefined, slug: string, refreshKey?: unknown): Check[] | null {
  const [state, setState] = useState<{ slug: string; checks: Check[] } | null>(null)
  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/checkup`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : null))
      .then((body: { checks?: ServerCheck[] } | null) => {
        if (controller.signal.aborted || !Array.isArray(body?.checks)) return
        setState({
          slug,
          checks: body!.checks!.map((c) => ({
            key: c.key,
            tone: c.level,
            title: c.title,
            detail: c.detail,
            ...(c.tab && FIX_LABEL[c.tab] && c.level !== "ok" ? { fix: { label: FIX_LABEL[c.tab], tab: c.tab as "overview" | "steps" | "triggers" | "settings" | "versions" } } : {}),
          })),
        })
      })
      .catch(() => {})
    return () => controller.abort()
  }, [workspaceId, slug, refreshKey])
  return state?.slug === slug ? state.checks : null
}
