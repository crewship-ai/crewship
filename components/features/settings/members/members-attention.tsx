"use client"

import type { CSSProperties } from "react"
import { Check, ChevronRight, Info, TriangleAlert } from "lucide-react"

import { StatusPill } from "@/components/ui/status-pill"
import { toStatusTone } from "@/lib/status-tone"
import { SettingsCard } from "@/components/features/settings/shared"

export interface Finding {
  key: string
  severity: "high" | "medium" | "low"
  title: string
  body: string
  verb: string
  run: () => void
}

const TINT: Record<Finding["severity"], string> = { high: "var(--destructive)", medium: "var(--warn)", low: "var(--info)" }

/**
 * A card of findings with one verb each. Settings › Members no longer draws
 * it (its findings live in the "Needs a look" view); Admin › People mounts it
 * with its own findings, so it stays here.
 */
export function MembersAttention({ findings }: { findings: Finding[] }) {
  return (
    <SettingsCard icon={TriangleAlert} tint={findings.length ? "var(--warn)" : "var(--success)"} title="Needs attention"
      description={findings.length ? `${findings.length} ${findings.length === 1 ? "thing" : "things"} about who can do what here` : "Who can do what in this workspace"}>
      {findings.length === 0 ? (
        <p className="flex items-center gap-2 px-4 py-3.5 text-label text-muted-foreground" data-slot="members-all-clear">
          <Check className="h-3.5 w-3.5 text-success" aria-hidden />Nothing needs attention — nobody holds more than their role suggests.
        </p>
      ) : findings.map((f) => (
        <div key={f.key} data-finding={f.key} className="flex items-center gap-3 border-b border-border/60 px-4 py-3 last:border-b-0">
          <span className="icon-tile grid h-7 w-7 shrink-0 place-items-center rounded-lg" style={{ "--ic": TINT[f.severity] } as CSSProperties} aria-hidden>
            {f.severity === "low" ? <Info className="h-3.5 w-3.5" /> : <TriangleAlert className="h-3.5 w-3.5" />}
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <StatusPill tone={toStatusTone(f.severity)} label={f.severity.charAt(0).toUpperCase() + f.severity.slice(1)} />
              <p className="min-w-0 text-label font-medium leading-snug text-foreground">{f.title}</p>
            </div>
            <p className="mt-0.5 text-label leading-snug text-muted-foreground">{f.body}</p>
          </div>
          <button type="button" onClick={f.run}
            className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border px-2 py-1 text-label font-medium text-foreground/90 transition-colors hover:border-primary/50 hover:text-primary-hover">
            {f.verb}<ChevronRight className="h-3 w-3" aria-hidden />
          </button>
        </div>
      ))}
    </SettingsCard>
  )
}
