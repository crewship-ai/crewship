"use client"

import { useEffect, useState } from "react"
import { BookOpen, Bug, ExternalLink, Sparkles } from "lucide-react"

import { CrewshipLogo, CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar"
import { DOCS_URL, RELEASES_URL, SUPPORT_URL } from "@/lib/crewship-links"
import { RAIL_ROW } from "@/components/layout/rail"
import { apiFetch } from "@/lib/api-fetch"
import { buildLabel, editionName, type Build } from "@/lib/build-info"

/**
 * The Crewship sail at the foot of the rail, above the sidebar-mode row
 * (owner, 19 Oct; wireframe round 61). It stays when the rail folds to icons,
 * so the product is named on every screen, and it opens a short About: what
 * Crewship is, which build this is, where to read more. The top of the rail
 * is the workspace switcher and Search only: folded, the sail, the workspace
 * and Search stacked in one column read as noise. The tile is the brand's own
 * lit blue and does not follow the chosen style.
 */
export function CrewshipBrand() {
  const [open, setOpen] = useState(false)
  return (
    <SidebarMenuItem>
      <SidebarMenuButton tooltip="About Crewship" aria-label="About Crewship" className={RAIL_ROW} onClick={() => setOpen(true)}>
        <span className="rail-tile rail-brand" aria-hidden>
          <CrewshipLogo tight />
        </span>
        <span className="rail-label min-w-0 flex-1 truncate text-left text-control font-semibold tracking-tight">Crewship</span>
      </SidebarMenuButton>
      <AboutCrewship open={open} onOpenChange={setOpen} />
    </SidebarMenuItem>
  )
}

const LINKS = [
  { label: "Documentation", href: DOCS_URL, icon: BookOpen },
  { label: "What's new", href: RELEASES_URL, icon: Sparkles },
  { label: "Report a problem", href: SUPPORT_URL, icon: Bug },
]

function AboutCrewship({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [build, setBuild] = useState<Build | null>(null)
  const [edition, setEdition] = useState<string | undefined>()
  useEffect(() => {
    if (!open || build) return
    let live = true
    apiFetch("/api/v1/system/version")
      .then((r) => (r.ok ? r.json() : null))
      .then((b) => { if (live && b?.current) setBuild(b) })
      .catch(() => {})
    apiFetch("/api/v1/system/license")
      .then((r) => (r.ok ? r.json() : null))
      .then((l) => { if (live && l?.edition) setEdition(l.edition) })
      .catch(() => {})
    return () => { live = false }
  }, [open, build])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader className="flex-row items-center gap-3 space-y-0">
          <CrewshipLogoTile size="h-10 w-10" rounded="rounded-xl" iconSize="h-5 w-5" />
          <div className="min-w-0">
            <DialogTitle>About Crewship</DialogTitle>
            {build && (
              <p className="text-xs text-muted-foreground" data-testid="about-build">
                {editionName(edition)} · {buildLabel(build)}
              </p>
            )}
          </div>
        </DialogHeader>
        <DialogDescription className="text-sm leading-relaxed text-foreground">
          Crewship runs AI agents for your team on your own hardware. Each crew works in its own isolated container,
          and one place holds the issues they work on, the routines they run, the approvals they ask for, what they
          cost and a journal of every step.
        </DialogDescription>
        {build?.newer && build.latest && (
          <p className="text-xs text-muted-foreground">
            A newer release is out: <span className="font-medium text-foreground">{build.latest}</span>.
          </p>
        )}
        <ul className="-mx-2 grid gap-px">
          {LINKS.map(({ label, href, icon: Icon }) => (
            <li key={label}>
              <a href={href} target="_blank" rel="noopener noreferrer"
                className="flex min-h-9 coarse:min-h-[44px] items-center gap-2.5 rounded-md px-2 text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                <Icon className="size-4 text-muted-foreground" aria-hidden />
                <span className="flex-1">{label}</span>
                <ExternalLink className="size-3.5 text-muted-foreground" aria-hidden />
              </a>
            </li>
          ))}
        </ul>
      </DialogContent>
    </Dialog>
  )
}
