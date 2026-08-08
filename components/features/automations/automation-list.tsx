"use client"

// The read-only view of a set of automation rules, shared by the two pages
// that have to explain one.
//
// There is no automation management screen — `crewship automation …` is the
// entire write surface — so this list's job is not to be a stub for one. It
// answers three things and stops: what the rule is called, what event it
// watches, and whether it is armed. Anything past that (matcher internals,
// debounce windows, hourly caps) belongs where you can change it.
//
// A disabled rule is shown, greyed, rather than filtered out. "Why did nothing
// happen" is the question these pages exist to answer, and a rule that is
// switched off is usually the answer.

import * as React from "react"
import { Zap, ZapOff } from "lucide-react"

import { cn } from "@/lib/utils"
import type { Automation } from "@/lib/automations"

/** Where automations are managed. There is no screen; this is the surface. */
export const AUTOMATIONS_DOCS_URL = "https://docs.crewship.ai/guides/automations"

export function AutomationList({
  automations,
  /** Rendered under the rows — the caller's own sentence about scope. */
  note,
  className,
}: {
  automations: Automation[]
  note?: React.ReactNode
  className?: string
}) {
  if (automations.length === 0) return null

  return (
    <div className={cn("space-y-2.5", className)}>
      <ul className="space-y-2.5 text-[12px]">
        {automations.map((a) => (
          <li
            key={a.id}
            data-testid={`automation-row-${a.id}`}
            className="flex items-start gap-2"
          >
            {a.enabled ? (
              <Zap className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            ) : (
              <ZapOff className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
            )}
            <div className="min-w-0 flex-1">
              <div
                className={cn(
                  "truncate",
                  a.enabled ? "text-foreground/90" : "text-muted-foreground",
                )}
              >
                {a.name}
              </div>
              <div className="flex flex-wrap items-baseline gap-x-2 text-[10px] text-muted-foreground">
                {/* The event type is the rule, more than its name is: a rule
                    called "Triage" tells you nothing about when it fires. */}
                <span className="font-mono">{a.event_type}</span>
                {!a.enabled && (
                  <>
                    <span aria-hidden>·</span>
                    <span>disabled</span>
                  </>
                )}
              </div>
            </div>
          </li>
        ))}
      </ul>
      {note}
      {/* Named, not linked, because the CLI is where these are edited. A link
          to a screen that does not exist would be the worse lie. */}
      <p className="text-[11px] text-muted-foreground">
        Managed with <span className="font-mono text-foreground/80">crewship automation</span> —{" "}
        <a
          href={AUTOMATIONS_DOCS_URL}
          target="_blank"
          rel="noreferrer"
          className="text-primary hover:underline"
        >
          docs
        </a>
      </p>
    </div>
  )
}
