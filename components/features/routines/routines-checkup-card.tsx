"use client"

import * as React from "react"
import { ListChecks } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DashCard } from "@/components/features/dashboard/dash-card"
import { routineChecks, type HealthRoutine, type HealthRun } from "@/lib/routine-health"
import { RoutineGlyph } from "./routine-glyph"

// Checkup across routines (Harbor 2 RT-06, v67 "Checkup across routines"):
// only the routines that have something to fix, one line per failed check,
// and one sentence for the ones that only have something to look at. A
// routine that passes is not listed. Not drawn when nothing is to fix.

export function RoutinesCheckupCard({
  routines,
  runs,
  onSelect,
}: {
  routines: (HealthRoutine & { id?: string })[]
  runs: HealthRun[]
  onSelect: (slug: string) => void
}) {
  const { lines, toCheck } = React.useMemo(() => {
    const lines: { key: string; routine: HealthRoutine & { id?: string }; title: string; detail: string }[] = []
    let toCheck = 0
    for (const routine of routines) {
      const mine = runs.filter((r) => r.pipeline_slug === routine.slug)
      const checks = routineChecks({ routine, runs: mine })
      for (const c of checks) {
        if (c.tone === "bad") lines.push({ key: `${routine.slug}:${c.key}`, routine, title: c.title, detail: c.detail })
        else if (c.tone === "warn") toCheck += 1
      }
    }
    return { lines, toCheck }
  }, [routines, runs])
  if (lines.length === 0) return null
  return (
    <DashCard title="Checkup across routines" icon={ListChecks} meta={String(lines.length)} data-testid="routines-checkup">
      <ul className="px-2 pb-1">
        {lines.map((l) => (
          <li key={l.key} className="flex flex-wrap items-center gap-2.5 border-b border-hairline px-2 py-2 last:border-0 coarse:min-h-[44px]">
            <RoutineGlyph routine={l.routine} size="sm" />
            <span className="min-w-0 flex-1 basis-48">
              <span className="block truncate text-body font-medium text-foreground">
                {l.routine.name || l.routine.slug} · {l.title}
              </span>
              <span className="block text-label text-muted-foreground">{l.detail}</span>
            </span>
            <Button type="button" size="sm" variant="outline" className="coarse:h-[44px]" onClick={() => onSelect(l.routine.slug)}>
              Open
            </Button>
          </li>
        ))}
      </ul>
      {toCheck > 0 && (
        <p className="px-4 pb-3 text-label text-muted-foreground">
          {toCheck} more to check inside the routines. The checks are computed from each routine&apos;s status, draft and newest runs.
        </p>
      )}
    </DashCard>
  )
}
