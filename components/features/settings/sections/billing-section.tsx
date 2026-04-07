"use client"

import { CreditCard } from "lucide-react"
import { AnimatedNumber } from "@/components/ui/animated-number"

interface BillingSectionProps {
  agentCount: number
  crewCount: number
  memberCount: number
  workspaceName: string
}

export function BillingSection({ agentCount, crewCount, memberCount, workspaceName }: BillingSectionProps) {
  return (
    <div className="space-y-4">
      {/* Usage stats */}
      <div className="bg-card border border-white/[0.06] rounded-lg p-6">
        <h4 className="text-[11px] font-semibold text-muted-foreground/50 uppercase tracking-wider mb-1">
          Workspace Usage
        </h4>
        <p className="text-[12px] text-muted-foreground/40 mb-5">{workspaceName}</p>

        <div className="grid grid-cols-3 gap-4">
          {[
            { label: "Agents", value: agentCount, color: "bg-blue-500" },
            { label: "Crews", value: crewCount, color: "bg-emerald-500" },
            { label: "Members", value: memberCount, color: "bg-cyan-500" },
          ].map(({ label, value, color }) => (
            <div key={label} className="bg-white/[0.02] border border-white/[0.06] rounded-lg p-4">
              <div className="flex items-center gap-1.5 mb-2">
                <div className={`w-1.5 h-1.5 rounded-full ${color}`} />
                <span className="text-[10px] text-muted-foreground/50 uppercase tracking-wider font-medium">
                  {label}
                </span>
              </div>
              <div className="text-[24px] font-mono font-semibold text-foreground tabular-nums">
                <AnimatedNumber value={value} />
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Billing placeholder */}
      <div className="bg-card border border-white/[0.06] rounded-lg p-8 text-center">
        <CreditCard className="h-8 w-8 text-muted-foreground/20 mx-auto mb-3" />
        <p className="text-[13px] text-muted-foreground/50">
          Billing and plan management will be available in a future release.
        </p>
      </div>
    </div>
  )
}
