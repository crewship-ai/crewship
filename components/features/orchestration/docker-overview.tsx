"use client"

import { Container, Info } from "lucide-react"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Table, TableHeader, TableBody, TableHead, TableRow, TableCell,
} from "@/components/ui/table"
import { resolveCrewColor } from "@/lib/colors"
import type { CrewSummary } from "@/lib/types/orchestration"

export interface DockerOverviewProps {
  crews: CrewSummary[]
}

/**
 * Docker container overview table.
 * NOTE: Real Docker API metrics (CPU, RAM, network) are not yet available
 * on the frontend. This component renders placeholder data as a UI scaffold.
 * Replace placeholders once a /api/v1/containers endpoint exists.
 */
export function DockerOverview({ crews }: DockerOverviewProps) {
  if (crews.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full py-8 text-muted-foreground/70">
        <Container className="size-6 mb-2" />
        <p className="text-xs">No crews configured</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      <ScrollArea className="flex-1 min-h-0">
        <Table>
          <TableHeader>
            <TableRow className="border-border hover:bg-transparent">
              <TableHead className="text-muted-foreground text-[11px] font-medium">Container</TableHead>
              <TableHead className="text-muted-foreground text-[11px] font-medium text-right">Agents</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {crews.map(crew => (
              <TableRow key={crew.id} className="border-border hover:bg-accent/30">
                <TableCell className="font-mono text-[11px] text-foreground/80">
                  <div className="flex items-center gap-1.5">
                    {crew.color && (
                      <span className="size-2 rounded-full" style={{ backgroundColor: resolveCrewColor(crew.color) }} />
                    )}
                    crewship-team-{crew.slug}
                  </div>
                </TableCell>
                <TableCell className="text-[11px] text-muted-foreground text-right">
                  {crew._count?.agents ?? 0}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </ScrollArea>
      {/* The old table fabricated Image ("node:18-slim"), an always-green
          Running status, and dashed CPU/RAM/Network cells — it read as live
          infrastructure data while only the container name and agent count
          were real. Until a real /containers stats endpoint exists, show
          only the real columns and say so plainly. */}
      <div className="flex items-center gap-1.5 px-3 py-1.5 border-t border-border text-muted-foreground/50 text-[10px] shrink-0">
        <Info className="size-3" />
        Live container metrics (image, status, CPU/RAM/network) are not
        available yet
      </div>
    </div>
  )
}
