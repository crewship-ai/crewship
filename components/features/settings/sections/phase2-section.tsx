"use client"

import { Badge } from "@/components/ui/badge"

export function Phase2Section() {
  return (
    <div className="bg-card border border-white/[0.06] rounded-lg p-8 text-center">
      <Badge
        variant="outline"
        className="mb-3 text-[10px] bg-white/[0.04] border-white/[0.08] text-muted-foreground/60"
      >
        Phase 2
      </Badge>
      <p className="text-[13px] text-muted-foreground/50">
        This feature is coming in a future release.
      </p>
    </div>
  )
}
