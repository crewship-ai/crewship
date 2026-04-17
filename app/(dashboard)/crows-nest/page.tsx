"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Binoculars, ArrowRight, Loader2 } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"

interface CrewSummary {
  id: string
  name: string
  slug: string
  _count?: { agents: number }
}

/**
 * Crow's Nest index — the page itself is a crew picker; the per-crew telemetry
 * lives at `/crows-nest/[crewId]`. Admin-only (same gate as the per-crew page).
 */
export default function CrowsNestIndexPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { role, loading: rolesLoading } = useAbilities()
  const [crews, setCrews] = useState<CrewSummary[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        if (!res.ok) return
        const json = await res.json()
        if (!cancelled && Array.isArray(json)) setCrews(json)
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading])

  const isAdmin = role === "OWNER" || role === "ADMIN"

  if (wsLoading || rolesLoading || loading) {
    return (
      <div className="flex items-center justify-center py-20 text-muted-foreground">
        <Loader2 className="h-4 w-4 mr-2 animate-spin" /> Loading…
      </div>
    )
  }

  if (!isAdmin) {
    return <AdminOnlyMessage />
  }

  return (
    <div className="p-4 md:p-6 space-y-4">
      <div className="flex items-center gap-2">
        <Binoculars className="h-4 w-4 text-foreground/60" />
        <h1 className="text-body font-medium text-foreground/80">Crow&apos;s Nest</h1>
      </div>
      <p className="text-[12px] text-muted-foreground max-w-2xl">
        Pick a crew to watch its live container activity — exec output, network, filesystem, and resource metrics.
      </p>

      {crews.length === 0 ? (
        <div className="flex flex-col items-center gap-2 py-16 text-center">
          <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
            <Binoculars className="h-4 w-4 text-muted-foreground/60" />
          </div>
          <div className="text-sm font-medium text-foreground/80">No crews yet</div>
          <div className="text-[11px] text-muted-foreground max-w-sm">
            Create a crew to enable the Crow&apos;s Nest.
          </div>
        </div>
      ) : (
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {crews.map((crew) => (
            <Card key={crew.id} className="hover:border-border transition-colors py-4">
              <CardContent className="flex items-center gap-3 px-4">
                <div className="min-w-0 flex-1">
                  <div className="text-sm font-medium truncate">{crew.name}</div>
                  <div className="text-[11px] text-muted-foreground font-mono truncate">{crew.slug}</div>
                </div>
                <Button variant="outline" size="sm" className="h-7 px-2 text-xs" asChild>
                  <Link href={`/crows-nest/${crew.id}`}>
                    Watch <ArrowRight className="h-3 w-3 ml-1" />
                  </Link>
                </Button>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}

function AdminOnlyMessage() {
  return (
    <div className="flex flex-col items-center gap-2 py-20 text-center">
      <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
        <Binoculars className="h-4 w-4 text-muted-foreground/60" />
      </div>
      <div className="text-sm font-medium text-foreground/80">Crow&apos;s Nest requires admin role</div>
      <div className="text-[11px] text-muted-foreground max-w-sm">
        Only workspace owners and admins can watch live crew container activity.
      </div>
    </div>
  )
}
