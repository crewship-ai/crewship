"use client"

import * as React from "react"
import { ArrowLeft, Bell, Check, KeyRound, Search, Wrench } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { cn } from "@/lib/utils"
import type { NotificationProviderCategory } from "@/hooks/use-notification-channels"
import { ProviderMark } from "./provider-marks"

/**
 * "Add integration" — one door for everything this instance can connect to.
 *
 * The catalog used to be a permanent tab, which put a browse-only surface on
 * equal footing with the four tabs you actually work in, and it answered a
 * question you only ask while adding something. Worse, it flattened two
 * unrelated kinds of integration into one grid: a Slack webhook and a managed
 * tool account have nothing in common beyond the word "integration".
 *
 * So the first step is the KIND, and the kinds are a list — adding a third
 * (an identity provider, a log sink, whatever comes) is one entry here, not a
 * new tab and not a new grid to reconcile with the old one.
 */

export type IntegrationKind = "notification" | "tools"

interface KindOption {
  key: IntegrationKind
  label: string
  /** What it does, in the user's terms — not "channel" or "MCP server". */
  blurb: string
  /** The one line that tells you whether this is the one you want. */
  distinguisher: string
  icon: LucideIcon
  accent: string
}

const KINDS: KindOption[] = [
  {
    key: "notification",
    label: "Notifications",
    blurb: "Chat, push, on-call, e-mail or your own endpoint",
    distinguisher: "Somewhere Crewship reaches a person",
    icon: Bell,
    accent: "#1E7BFE",
  },
  {
    key: "tools",
    label: "Tools & MCP",
    blurb: "Managed app accounts your agents can call",
    distinguisher: "Something an agent can act through",
    icon: Wrench,
    accent: "#8B5CF6",
  },
]

/** A service the notification step can offer. */
export interface ServiceOption {
  key: string
  label: string
  blurb: string
  section: string
  available: boolean
  used: number
}

interface AddIntegrationDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Notification services, already grouped by the server's categories. */
  services: ServiceOption[]
  sections: NotificationProviderCategory[]
  /** Chosen a notification service — the host opens its form. */
  onPickService: (service: ServiceOption) => void
  /** Chosen tools — the host switches to that tab (or opens the key dialog). */
  onPickTools: () => void
  /** false = Composio has no API key yet, so tools needs setup first. */
  toolsConfigured: boolean
}

export function AddIntegrationDialog({
  open,
  onOpenChange,
  services,
  sections,
  onPickService,
  onPickTools,
  toolsConfigured,
}: AddIntegrationDialogProps) {
  const [kind, setKind] = React.useState<IntegrationKind | null>(null)
  const [query, setQuery] = React.useState("")

  // Every open starts at the first question. Resuming mid-wizard sounds
  // helpful and is not: you reopen it to add a *different* thing.
  React.useEffect(() => {
    if (open) {
      setKind(null)
      setQuery("")
    }
  }, [open])

  const q = query.trim().toLowerCase()
  const matching = services.filter(
    (s) =>
      !q ||
      s.label.toLowerCase().includes(q) ||
      s.blurb.toLowerCase().includes(q) ||
      s.key.toLowerCase().includes(q) ||
      (sections.find((sec) => sec.key === s.section)?.label ?? "").toLowerCase().includes(q),
  )

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col overflow-hidden sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-sm">
            {kind && (
              <button
                type="button"
                onClick={() => setKind(null)}
                aria-label="Back to the kind of integration"
                className="-ml-1 rounded p-0.5 text-muted-foreground transition-colors hover:text-foreground"
              >
                <ArrowLeft className="h-3.5 w-3.5" />
              </button>
            )}
            {kind === null
              ? "Add an integration"
              : kind === "notification"
                ? "Pick a notification service"
                : "Tools & MCP"}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {kind === null
              ? "Two kinds of thing live here. Which one are you connecting?"
              : kind === "notification"
                ? "Where should Crewship reach you? You can add more later."
                : "Managed accounts your agents call on a person's behalf."}
          </DialogDescription>
        </DialogHeader>

        {kind === null && (
          <div className="grid gap-3 sm:grid-cols-2">
            {KINDS.map((k) => {
              const Icon = k.icon
              return (
                <button
                  key={k.key}
                  type="button"
                  onClick={() => {
                    if (k.key === "tools") {
                      onOpenChange(false)
                      onPickTools()
                      return
                    }
                    setKind(k.key)
                  }}
                  className={cn(
                    "flex flex-col items-start gap-2 rounded-xl border bg-card px-4 py-4 text-left",
                    "border-white/[0.08] transition-colors hover:border-primary/40 hover:bg-white/[0.02]",
                  )}
                >
                  <span
                    className="flex h-8 w-8 items-center justify-center rounded-lg"
                    style={{
                      backgroundColor: `color-mix(in oklab, ${k.accent} 18%, transparent)`,
                      boxShadow: `inset 0 0 0 1px color-mix(in oklab, ${k.accent} 35%, transparent)`,
                    }}
                  >
                    <Icon className="h-4 w-4" style={{ color: k.accent }} />
                  </span>
                  <span className="text-sm font-medium text-foreground/90">{k.label}</span>
                  <span className="text-[11px] leading-snug text-muted-foreground">{k.blurb}</span>
                  <span className="mt-1 text-[11px] font-medium text-foreground/60">
                    {k.distinguisher}
                  </span>
                  {k.key === "tools" && !toolsConfigured && (
                    <span className="inline-flex items-center gap-1 rounded-full border border-warn/30 bg-warn/10 px-1.5 py-0.5 font-mono text-[10px] text-warn">
                      <KeyRound className="h-2.5 w-2.5" />
                      needs an API key
                    </span>
                  )}
                </button>
              )
            })}
          </div>
        )}

        {kind === "notification" && (
          <>
            <div className="flex items-center gap-1.5 rounded-md border border-white/[0.08] bg-white/[0.04] px-2.5 py-1.5">
              <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground/50" />
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                autoFocus
                placeholder="Search services…"
                aria-label="Search notification services"
                className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground/40"
              />
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground/50">
                {matching.length}/{services.length}
              </span>
            </div>

            <div className="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
              {matching.length === 0 ? (
                <p className="px-2 py-10 text-center text-xs text-muted-foreground">
                  Nothing matches “{query.trim()}”. This instance ships {services.length} services.
                </p>
              ) : (
                sections.map((section) => {
                  const items = matching.filter((s) => s.section === section.key)
                  if (items.length === 0) return null
                  return (
                    <section key={section.key} className="mb-4">
                      <div className="mb-1.5 flex items-baseline gap-2">
                        <h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
                          {section.label}
                        </h3>
                        <span className="tabular-nums text-[10px] text-muted-foreground/50">
                          {items.length}
                        </span>
                        {section.hint && (
                          <span className="hidden text-[11px] text-muted-foreground/60 sm:inline">
                            · {section.hint}
                          </span>
                        )}
                      </div>
                      <div className="grid gap-2 sm:grid-cols-2">
                        {items.map((s) => (
                          <button
                            key={s.key}
                            type="button"
                            disabled={!s.available}
                            onClick={() => {
                              onOpenChange(false)
                              onPickService(s)
                            }}
                            title={
                              s.available
                                ? `Connect ${s.label}`
                                : `${s.label} is switched off on this instance`
                            }
                            className={cn(
                              "flex items-start gap-2.5 rounded-lg border bg-card px-3 py-2.5 text-left transition-colors",
                              s.available
                                ? "border-white/[0.08] hover:border-primary/40 hover:bg-white/[0.02]"
                                : "cursor-not-allowed border-white/[0.05] opacity-45",
                            )}
                          >
                            <ProviderMark provider={s.key} label={s.label} className="h-6 w-6" />
                            <span className="min-w-0 flex-1">
                              <span className="block truncate text-xs font-medium text-foreground/90">
                                {s.label}
                              </span>
                              <span className="mt-0.5 block text-[11px] leading-snug text-muted-foreground">
                                {s.blurb}
                              </span>
                            </span>
                            {s.used > 0 && (
                              <span className="inline-flex shrink-0 items-center gap-1 self-center rounded-full border border-success/25 bg-success/10 px-1.5 py-0.5 font-mono text-[10px] text-success">
                                <Check className="h-2.5 w-2.5" />
                                {s.used}
                              </span>
                            )}
                          </button>
                        ))}
                      </div>
                    </section>
                  )
                })
              )}

              {/* A greyed-out service is an instance decision, not a bug in
                  this dialog — say where it was made instead of leaving the
                  reader to guess why Slack is not clickable. */}
              {matching.some((s) => !s.available) && (
                <p className="px-2 pb-2 pt-1 text-[11px] text-muted-foreground/70">
                  Dimmed services are switched off for this instance. An admin can enable them in{" "}
                  <span className="text-foreground/70">Admin → Notifications</span>.
                </p>
              )}
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** The two kinds, exported so a caller can label things consistently. */
export { KINDS as INTEGRATION_KINDS }
