"use client"

import Link from "next/link"
import { AlertTriangle, KeyRound, CheckCircle2, FileText, AtSign, Zap } from "lucide-react"
import { cn } from "@/lib/utils"

interface ActionCenterProps {
  escalations: number
  keeperRequests: number
  missionsInReview: number
  proposals: number
  mentions: number
}

type Severity = "urgent" | "wait" | "info"

interface Chip {
  href: string
  label: string
  count: number
  severity: Severity
  icon: React.ElementType
}

export function ActionCenter({
  escalations,
  keeperRequests,
  missionsInReview,
  proposals,
  mentions,
}: ActionCenterProps) {
  const chips: Chip[] = [
    { href: "/orchestration?filter=escalations", label: "Escalations", count: escalations, severity: "urgent", icon: AlertTriangle },
    { href: "/credentials?filter=pending", label: "Keeper requests", count: keeperRequests, severity: "wait", icon: KeyRound },
    { href: "/orchestration?status=REVIEW", label: "Missions in review", count: missionsInReview, severity: "wait", icon: CheckCircle2 },
    { href: "/orchestration?tab=proposals", label: "Mission proposals", count: proposals, severity: "info", icon: FileText },
    { href: "/notifications?kind=mention", label: "Mentions", count: mentions, severity: "info", icon: AtSign },
  ]

  const total = chips.reduce((a, c) => a + c.count, 0)

  // Empty state — everything's clear
  if (total === 0) {
    return (
      <div className="flex items-center gap-3 px-4 py-3 mb-3 rounded-lg border border-emerald-500/20 bg-emerald-500/[0.04]">
        <CheckCircle2 className="h-4 w-4 text-emerald-400 shrink-0" />
        <span className="text-[11px] font-medium text-emerald-400/90">All clear — nothing is waiting on you right now.</span>
      </div>
    )
  }

  return (
    <div
      className="flex items-center gap-2 px-3.5 py-2.5 mb-3 rounded-lg border"
      style={{
        borderColor: "rgba(251, 146, 60, 0.18)",
        background: "linear-gradient(90deg, rgba(251,146,60,0.08), rgba(251,191,36,0.04))",
      }}
    >
      <div className="flex items-center gap-1.5 pr-3 mr-1 border-r border-white/[0.08] shrink-0">
        <Zap className="h-3.5 w-3.5 text-orange-400" />
        <span className="text-[11px] font-semibold text-orange-400">Waiting for you</span>
      </div>
      <div className="flex items-center gap-1.5 flex-wrap flex-1">
        {chips.map((chip) => {
          if (chip.count === 0) return null
          const Icon = chip.icon
          return (
            <Link
              key={chip.label}
              href={chip.href}
              className={cn(
                "group inline-flex items-center gap-1.5 pl-1 pr-2 py-0.5 rounded-full border transition-colors",
                "bg-card hover:bg-card/60 border-white/[0.08] hover:border-white/[0.12]",
              )}
            >
              <span
                className={cn(
                  "inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded-full text-[10px] font-mono font-semibold",
                  chip.severity === "urgent" && "bg-red-500/18 text-red-400",
                  chip.severity === "wait" && "bg-amber-500/18 text-amber-400",
                  chip.severity === "info" && "bg-blue-500/18 text-blue-400",
                )}
              >
                {chip.count}
              </span>
              <Icon className="h-3 w-3 text-foreground/60 group-hover:text-foreground/80" />
              <span className="text-[11px] text-foreground/80">{chip.label}</span>
            </Link>
          )
        })}
      </div>
      <span className="text-[10px] text-foreground/40 shrink-0 hidden sm:inline">Updated just now</span>
    </div>
  )
}
