"use client"

import Link from "next/link"
import { CalendarClock } from "lucide-react"

import { Button } from "@/components/ui/button"

/** The note on Model & limits: where schedules and webhooks live now. */
export function RoutinesMovedNote() {
  return (
    <div className="flex items-start gap-2.5 rounded-card border border-border bg-card px-4 py-3 text-body text-muted-foreground">
      <CalendarClock className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span className="min-w-0 flex-1">
        <b className="block font-medium text-foreground">Schedules and webhooks moved to Routines</b>
        <span>A routine runs this agent on a schedule, a webhook or an issue, with a test run and history.</span>
      </span>
      <Button asChild variant="ghost" size="sm"><Link href="/routines">Routines</Link></Button>
    </div>
  )
}
