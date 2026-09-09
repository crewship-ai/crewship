"use client"

import { CalendarDays, ShieldCheck, Users } from "lucide-react"

/** Metadata only: never accept a secret value or imply effective runtime access. */
export function CredentialSetupSummary({ scope, crewCount, tier, expiresAt }: {
  scope: "WORKSPACE" | "CREW"
  crewCount: number
  tier?: number
  expiresAt?: string
}) {
  const items = [
    { icon: Users, label: "Scope", value: scope === "WORKSPACE" ? "Workspace" : `${crewCount} selected crews` },
    ...(tier == null ? [] : [{ icon: ShieldCheck, label: "Protection", value: `Keeper L${tier}` }]),
    ...(expiresAt === undefined ? [] : [{ icon: CalendarDays, label: "Expiry", value: expiresAt || "No expiry set" }]),
  ]
  return (
    <dl aria-label="Credential settings summary" className="grid grid-cols-1 gap-3 rounded-xl border border-border/60 bg-muted/20 p-3 sm:grid-cols-3">
      {items.map(({ icon: Icon, label, value }) => (
        <div key={label} className="flex min-w-0 items-start gap-2.5">
          <Icon aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0">
            <dt className="text-[11px] text-muted-foreground">{label}</dt>
            <dd className="break-words text-xs font-medium text-foreground">{value}</dd>
          </div>
        </div>
      ))}
    </dl>
  )
}
