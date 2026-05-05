"use client"

import * as React from "react"
import { BadgeCheck, ShieldCheck, Globe2 } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

export type TrustTier = "anthropic" | "crewship" | "community"

const META: Record<TrustTier, { label: string; tooltip: string; icon: React.ComponentType<{ className?: string }>; cls: string }> = {
  anthropic: {
    label: "Verified by Anthropic",
    tooltip: "Server is in the official Anthropic MCP Registry. Publisher domain and email verified.",
    icon: ShieldCheck,
    cls: "border-blue-400/40 text-blue-300 bg-blue-500/[0.05]",
  },
  crewship: {
    label: "Verified by Crewship",
    tooltip: "Reviewed for compatibility and security by the crewship team.",
    icon: BadgeCheck,
    cls: "border-emerald-400/40 text-emerald-300 bg-emerald-500/[0.05]",
  },
  community: {
    label: "Community",
    tooltip: "Not verified by Anthropic or Crewship. Review the source before installing.",
    icon: Globe2,
    cls: "border-white/15 text-muted-foreground bg-zinc-950",
  },
}

export interface TrustTierBadgeProps {
  tier: TrustTier
  size?: "sm" | "md"
  className?: string
}

export function TrustTierBadge({ tier, size = "sm", className }: TrustTierBadgeProps) {
  const m = META[tier] ?? META.community
  const Icon = m.icon
  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <Badge
            variant="outline"
            className={cn(
              "gap-1 cursor-help font-medium",
              m.cls,
              size === "sm" ? "text-[10px] h-5 px-1.5" : "text-xs h-6 px-2",
              className,
            )}
          >
            <Icon className={cn(size === "sm" ? "h-2.5 w-2.5" : "h-3 w-3")} />
            {m.label}
          </Badge>
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-xs">
          <p className="text-xs">{m.tooltip}</p>
          <a
            href="/docs/mcp-trust-tiers"
            className="text-[11px] text-blue-300 hover:underline mt-1 inline-block"
          >
            Read trust tier criteria →
          </a>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
