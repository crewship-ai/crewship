"use client"

// What a routine awaiting approval is actually asking for.
//
// The banner told a reviewer the routine was flagged because it
// "requires credentials". That is the risk CATEGORY the classifier
// emitted; the question the person with their finger over Approve has
// is WHICH ones — and which integrations, and which hosts it will
// reach. The routine declares all of it, and the banner was reading
// none of it.
//
// Read from the definition already loaded on the page rather than from
// a new field on the response: the definition is what will run, so it
// cannot disagree with itself, and a second copy on the wire could.

import * as React from "react"
import { Pill } from "@/components/ui/detail"

interface CredReq {
  type?: string
  scope?: string
}

export interface RoutineAskDefinition {
  credentials_required?: CredReq[]
  integrations_required?: string[]
  egress_targets?: string[]
  [k: string]: unknown
}

/** Credentials render "type:scope" — github and github:repo differ. */
function credentialLabels(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  const out: string[] = []
  for (const c of value) {
    if (!c || typeof c !== "object") continue
    const { type, scope } = c as CredReq
    if (typeof type !== "string" || type === "") continue
    out.push(typeof scope === "string" && scope !== "" ? `${type}:${scope}` : type)
  }
  return out
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((v): v is string => typeof v === "string" && v !== "")
}

export function RoutineProposalAsk({ definition }: { definition: RoutineAskDefinition | undefined }) {
  const groups = React.useMemo(() => {
    const d = definition ?? {}
    return [
      { key: "creds", label: "Credentials", values: credentialLabels(d.credentials_required), tone: "warn" as const },
      { key: "ints", label: "Integrations", values: stringList(d.integrations_required), tone: "purple" as const },
      { key: "egress", label: "Egress", values: stringList(d.egress_targets), tone: "destructive" as const },
    ].filter((g) => g.values.length > 0)
  }, [definition])

  if (groups.length === 0) return null

  return (
    <div className="mt-1.5 flex flex-col gap-1">
      {groups.map((g) => (
        <div key={g.key} className="flex flex-wrap items-center gap-1.5">
          <span className="text-[10px] uppercase tracking-wide text-warn/70">{g.label}</span>
          {g.values.map((v) => (
            <Pill key={v} tone={g.tone}>
              {v}
            </Pill>
          ))}
        </div>
      ))}
    </div>
  )
}
