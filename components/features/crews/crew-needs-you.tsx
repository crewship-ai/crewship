"use client"

import Link from "next/link"
import { AlertTriangle, Bot, Box, Inbox, KeyRound, Plug } from "lucide-react"
import { motion, useReducedMotion } from "motion/react"

import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { cn } from "@/lib/utils"
import type { CrewNeed } from "./crew-needs"

const ICON = { build: Box, credential: KeyRound, integration: Plug, agent: Bot, decision: Inbox } as const

export interface CrewNeedsYouProps {
  needs: CrewNeed[]
  /** Which row's action is in flight, if any. */
  busyId?: string | null
  onBuild: () => void
  onInstall: (need: CrewNeed & { action: { kind: "install" } }) => void
  /** Where "Inbox →" goes; the strip is a summary, the inbox is the queue. */
  inboxHref: string
}

/**
 * The strip at the top of a crew canvas: what needs a person, each row with
 * its verb. Renders nothing when there is nothing — a healthy crew has no
 * warning box, and never an empty one.
 */
export function CrewNeedsYou({ needs, busyId = null, onBuild, onInstall, inboxHref }: CrewNeedsYouProps) {
  const reduce = useReducedMotion()
  if (needs.length === 0) return null
  return (
    <DetailCard
      tone="warn"
      bare
      title="Needs you"
      subtitle={`${needs.length}`}
      icon={AlertTriangle}
      action={<Link href={inboxHref} className="text-label text-primary-hover hover:underline">Inbox →</Link>}
      data-testid="crew-needs-you"
    >
      <ul className="grid grid-cols-1 md:grid-cols-2">
        {needs.map((need, index) => {
          const Icon = ICON[need.icon]
          const busy = busyId === need.id
          const tone = need.tone === "danger" ? "text-destructive bg-destructive/15" : "text-warn bg-warn/15"
          return (
            <motion.li
              key={need.id}
              layout={reduce ? false : "position"}
              initial={reduce ? false : { opacity: 0, y: 4 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.2, delay: reduce ? 0 : Math.min(index, 8) * 0.045 }}
              className={cn(
                "flex items-center gap-3 px-4 py-2.5 border-t border-border/50",
                index % 2 === 1 && "md:border-l",
              )}
              data-tone={need.tone}
            >
              <span className={cn("inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md", tone)}>
                <Icon className="h-3.5 w-3.5" aria-hidden />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-body font-semibold text-foreground/90">{need.title}</span>
                <span className="block truncate text-label text-muted-foreground">{need.detail}</span>
              </span>
              {need.action.kind === "link" ? (
                <Button asChild variant="outline" size="sm" className="shrink-0">
                  <Link href={need.action.href}>{need.action.label}</Link>
                </Button>
              ) : need.action.kind === "build" ? (
                <Button variant="soft" size="sm" className="shrink-0" disabled={busy} onClick={onBuild}>
                  {busy ? "Starting…" : need.action.label}
                </Button>
              ) : (
                <Button
                  variant="soft"
                  size="sm"
                  className="shrink-0"
                  disabled={busy}
                  onClick={() => onInstall(need as CrewNeed & { action: { kind: "install" } })}
                >
                  {busy ? "Installing…" : need.action.label}
                </Button>
              )}
            </motion.li>
          )
        })}
      </ul>
    </DetailCard>
  )
}
