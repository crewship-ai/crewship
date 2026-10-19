"use client"

import * as React from "react"
import { AlertTriangle, CheckCircle2, XCircle } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import { useRoutineCheckup } from "@/hooks/use-routine-checkup"
import { checkupCounts, routineChecks, type Check, type CheckTone, type HealthRoutine, type HealthRun } from "@/lib/routine-health"

// Checkup (Harbor 2 RT-06). With a workspace the card shows the server's
// checklist (GET .../checkup: agents, credentials, cost cap, failures in a
// row, the schedule circuit breaker, untested changes). Until that answers, or
// if it cannot, it shows only what the page can prove itself: the routine's
// status and draft, its newest runs and the last failure.

const ICON: Record<CheckTone, { Icon: typeof CheckCircle2; cls: string }> = {
  ok: { Icon: CheckCircle2, cls: "text-success" },
  warn: { Icon: AlertTriangle, cls: "text-warn" },
  bad: { Icon: XCircle, cls: "text-destructive" },
}

type Tab = "overview" | "steps" | "triggers" | "settings" | "versions"

function CheckRow({ check, onTab }: { check: Check; onTab: (tab: Tab) => void }) {
  const { Icon, cls } = ICON[check.tone]
  return (
    <li className="flex flex-wrap items-start gap-x-3 gap-y-1 py-2" data-tone={check.tone}>
      <Icon className={cn("mt-0.5 h-4 w-4 shrink-0", cls)} aria-hidden />
      <span className="min-w-0 flex-1 basis-48">
        <b className="block text-sm font-medium">{check.title}</b>
        <small className="block text-xs text-muted-foreground">{check.detail}</small>
      </span>
      {check.fix && (
        <Button type="button" variant="outline" size="sm" className="coarse:h-[44px]" onClick={() => onTab(check.fix!.tab)}>
          {check.fix.label}
        </Button>
      )}
    </li>
  )
}

export function RoutineCheckupCard({ routine, runs, onTab, workspaceId }: { routine: HealthRoutine; runs: HealthRun[]; onTab: (tab: Tab) => void; workspaceId?: string }) {
  const local = React.useMemo(() => routineChecks({ routine, runs }), [routine, runs])
  const server = useRoutineCheckup(workspaceId, routine.slug, `${runs[0]?.id ?? ""}:${routine.status ?? ""}:${routine.draft?.revision ?? ""}`)
  const checks = server && server.length > 0 ? server : local
  const n = checkupCounts(checks)
  const [showOk, setShowOk] = React.useState(false)
  const issues = checks.filter((c) => c.tone !== "ok")
  const passed = checks.filter((c) => c.tone === "ok")
  const tone = n.bad ? "var(--destructive)" : n.warn ? "var(--warn)" : "var(--success)"
  return (
    <DetailCard title="Checkup" icon={CheckCircle2}>
      <div data-testid="checkup" className="flex flex-col gap-1">
        <div className="flex items-center gap-3">
          <span
            data-testid="checkup-circle"
            className="grid h-12 w-12 shrink-0 place-items-center rounded-full font-mono text-xs font-semibold tabular-nums"
            style={{ background: `conic-gradient(${tone} ${Math.round((n.ok / n.all) * 100)}%, transparent 0)`, boxShadow: `inset 0 0 0 2px ${tone}` }}
          >
            {n.ok}/{n.all}
          </span>
          <span>
            <b className="block text-sm font-medium">{n.bad ? `${n.bad} to fix` : n.warn ? `${n.warn} to check` : "Everything looks right"}</b>
            <small className="block text-xs text-muted-foreground">{server && server.length > 0 ? "Checked by the server, and again after every change and run." : "From what the page knows: status, changes and the latest runs."}</small>
          </span>
        </div>
        <ul className="divide-y divide-border/50">
          {issues.map((c) => <CheckRow key={c.key} check={c} onTab={onTab} />)}
          {showOk && passed.map((c) => <CheckRow key={c.key} check={c} onTab={onTab} />)}
        </ul>
        {passed.length > 0 && (
          <button type="button" className="w-fit text-xs text-primary-hover hover:underline coarse:min-h-[44px]" onClick={() => setShowOk((v) => !v)}>
            {showOk ? "Hide passed checks" : `${passed.length} ${passed.length === 1 ? "check" : "checks"} passed · show`}
          </button>
        )}
      </div>
    </DetailCard>
  )
}
