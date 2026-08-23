"use client"

import * as React from "react"
import { Bell, Check, KeyRound, Search, Wrench } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceSection,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"
import type { AccentName } from "@/lib/concept-accents"
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
 *
 * The chrome is `CreateSurface` (see `components/layout/create-surface.tsx`),
 * which is where the overlay, the width, the back arrow, the close and the
 * phone bottom-sheet now come from. Two things about this surface are worth
 * knowing before editing it:
 *
 *  · It CREATES NOTHING. Picking a notification service closes the dialog and
 *    hands the host a `ServiceOption` so it can open that service's own form;
 *    picking Tools closes it and switches tabs. So there is no footer, because
 *    there is no primary action to put in one — the tiles ARE the action, and
 *    a "Continue" button beside them would be a second, redundant way to do
 *    the same thing.
 *  · The search row sits BETWEEN the header and the body on purpose. The body
 *    is the shell's one scrollport; a filter that scrolls away from the list
 *    it filters is a filter you cannot see while reading the results.
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
  /**
   * A named accent, not a hex. Both colours are the same ones this file used
   * to inline (`#1E7BFE` is `--primary-hover`, `#8B5CF6` is `--purple`); going
   * through the accent map is what stops a thirteenth shade appearing the next
   * time a kind is added.
   */
  accent: AccentName
}

const KINDS: KindOption[] = [
  {
    key: "notification",
    label: "Notifications",
    blurb: "Chat, push, on-call, e-mail or your own endpoint",
    distinguisher: "Somewhere Crewship reaches a person",
    icon: Bell,
    accent: "blue",
  },
  {
    key: "tools",
    label: "Tools & MCP",
    blurb: "Managed app accounts your agents can call",
    distinguisher: "Something an agent can act through",
    icon: Wrench,
    accent: "purple",
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
    <CreateSurface open={open} onOpenChange={onOpenChange} size="xl">
      <CreateSurfaceHeader
        concept="integrations"
        context="Integrations"
        title={
          kind === null
            ? "Add an integration"
            : kind === "notification"
              ? "Pick a notification service"
              : "Tools & MCP"
        }
        description={
          kind === null
            ? "Two kinds of thing live here. Which one are you connecting?"
            : kind === "notification"
              ? "Where should Crewship reach you? You can add more later."
              : "Managed accounts your agents call on a person's behalf."
        }
        onBack={kind ? () => setKind(null) : undefined}
        onClose={() => onOpenChange(false)}
      />

      {kind === "notification" && (
        <div className="flex shrink-0 items-center gap-1.5 border-b border-hairline px-4 py-2 sm:px-5">
          <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground/50" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            autoFocus
            placeholder="Search services…"
            aria-label="Search notification services"
            className={cn(
              "min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground/40",
              "max-sm:h-12 max-sm:text-sm group-data-[mobile=true]/surface:h-12 group-data-[mobile=true]/surface:text-sm",
            )}
          />
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground/50">
            {matching.length}/{services.length}
          </span>
        </div>
      )}

      <CreateSurfaceBody>
        {kind === null && (
          <CreateSurfaceGrid>
            {KINDS.map((k, i) => (
              <CreateSurfaceTile
                key={k.key}
                // The shell does not autofocus — it leaves that to the surface
                // so the field people came to type in wins. This one has no
                // field, so the first choice takes it.
                autoFocus={i === 0}
                icon={k.icon}
                accent={k.accent}
                title={k.label}
                description={
                  <>
                    {k.blurb}
                    <span className="mt-1 block font-medium text-foreground/60">{k.distinguisher}</span>
                  </>
                }
                meta={
                  k.key === "tools" && !toolsConfigured ? (
                    <span className="inline-flex items-center gap-1 rounded-full border border-warn/30 bg-warn/10 px-1.5 py-0.5 font-mono text-[10px] text-warn">
                      <KeyRound className="h-2.5 w-2.5" />
                      needs an API key
                    </span>
                  ) : undefined
                }
                onClick={() => {
                  if (k.key === "tools") {
                    onOpenChange(false)
                    onPickTools()
                    return
                  }
                  setKind(k.key)
                }}
              />
            ))}
          </CreateSurfaceGrid>
        )}

        {kind === "notification" && (
          <div className="flex flex-col gap-5">
            {matching.length === 0 ? (
              <p className="px-2 py-10 text-center text-xs text-muted-foreground">
                Nothing matches “{query.trim()}”. This instance ships {services.length} services.
              </p>
            ) : (
              sections.map((section) => {
                const items = matching.filter((s) => s.section === section.key)
                if (items.length === 0) return null
                return (
                  <CreateSurfaceSection
                    key={section.key}
                    title={
                      <>
                        {section.label}
                        <span className="ml-1.5 font-normal tabular-nums text-muted-foreground/50">
                          {items.length}
                        </span>
                      </>
                    }
                    hint={section.hint}
                  >
                    <CreateSurfaceGrid>
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
                          // Geometry copied from CreateSurfaceTile rather than
                          // rendered by it: the tile's leading glyph goes
                          // through ConceptIcon, which draws its own accent
                          // chip, and a brand mark already carries its colour
                          // and its tile. See the report note on `leading`.
                          className={cn(
                            "flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors",
                            "max-sm:p-3.5 group-data-[mobile=true]/surface:p-3.5",
                            s.available
                              ? "border-hairline bg-foreground/[0.02] hover:border-border hover:bg-foreground/[0.05]"
                              : "cursor-not-allowed border-hairline bg-foreground/[0.02] opacity-45",
                          )}
                        >
                          <ProviderMark provider={s.key} label={s.label} className="h-8 w-8" />
                          <span className="min-w-0 flex-1">
                            <span className="flex items-center gap-2">
                              <span className="truncate text-[13px] font-medium text-foreground">
                                {s.label}
                              </span>
                              {s.used > 0 && (
                                <span className="ml-auto inline-flex shrink-0 items-center gap-1 rounded-full border border-success/25 bg-success/10 px-1.5 py-0.5 font-mono text-[10px] text-success">
                                  <Check className="h-2.5 w-2.5" />
                                  {s.used}
                                </span>
                              )}
                            </span>
                            <span className="mt-0.5 block text-xs leading-relaxed text-muted-foreground">
                              {s.blurb}
                            </span>
                          </span>
                        </button>
                      ))}
                    </CreateSurfaceGrid>
                  </CreateSurfaceSection>
                )
              })
            )}

            {/* A greyed-out service is an instance decision, not a bug in
                this dialog — say where it was made instead of leaving the
                reader to guess why Slack is not clickable. */}
            {matching.some((s) => !s.available) && (
              <p className="px-2 text-[11px] text-muted-foreground/70">
                Dimmed services are switched off for this instance. An admin can enable them in{" "}
                <span className="text-foreground/70">Admin → Notifications</span>.
              </p>
            )}
          </div>
        )}
      </CreateSurfaceBody>
    </CreateSurface>
  )
}

/** The two kinds, exported so a caller can label things consistently. */
export { KINDS as INTEGRATION_KINDS }
