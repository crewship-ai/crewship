"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { BookOpen, Bug, ExternalLink, Info, Sparkles } from "lucide-react"

import { CrewshipLogo, CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { BarMenu } from "@/components/layout/bar-menu"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { DOCS_URL, RELEASES_URL, SUPPORT_URL } from "@/lib/crewship-links"
import { apiFetch } from "@/lib/api-fetch"
import { buildLabel, editionName, type Build } from "@/lib/build-info"
import { useAccessMode } from "@/hooks/use-access-mode"
import { cn } from "@/lib/utils"

type VersionInfo = Build & { url?: string | null }

/**
 * Local-storage key for "this version was seen". Scoped by version, so a
 * reader who marks 0.1.5 as seen still gets the dot when 0.1.6 lands. The
 * prefix is the one the retired update banner's × wrote, so a version
 * dismissed there stays seen here.
 */
export const SEEN_KEY_PREFIX = "crewship.update-banner.dismissed:"

/**
 * The Crewship sail in the top bar (owner, 19 Nov; #5552), after Activity and
 * Inbox on every page and on phones. It replaced two things: the sail at the
 * foot of the rail, and the "Crewship X is available" banner that took a full
 * row under the top bar of every page. The tile is the brand's own lit blue
 * in every style and theme, and the only coloured mark in the strip, so the
 * product is recognisable at a glance. A newer release puts a dot on the
 * tile; the panel says which and links the release notes.
 */
export function CrewshipMenu() {
  // /system/version is not on the restricted allowlist; a restricted
  // session still gets the sail and About, without the version check.
  const trusted = useAccessMode() === "trusted"
  const [open, setOpen] = useState(false)
  const [aboutOpen, setAboutOpen] = useState(false)
  const [info, setInfo] = useState<VersionInfo | null>(null)
  const [edition, setEdition] = useState<string | undefined>()
  const [seen, setSeen] = useState(false)

  const check = useCallback(async () => {
    try {
      const res = await apiFetch("/api/v1/system/version")
      if (!res.ok) return
      const data = (await res.json()) as VersionInfo
      if (data?.current) setInfo(data)
    } catch {
      // Best-effort: a failed check must never surface as an error.
    }
  }, [])

  useEffect(() => {
    if (!trusted) return
    check()
    // Hourly, and on focus: the backend caches its own check for 24 h, so
    // this only reflects a release that lands while the tab stays open.
    const interval = setInterval(check, 60 * 60 * 1000)
    const onFocus = () => check()
    window.addEventListener("focus", onFocus)
    return () => {
      clearInterval(interval)
      window.removeEventListener("focus", onFocus)
    }
  }, [trusted, check])

  useEffect(() => {
    // /system/license is not on the restricted allowlist either.
    if (!trusted || !open || edition) return
    let live = true
    apiFetch("/api/v1/system/license")
      .then((r) => (r.ok ? r.json() : null))
      .then((l) => { if (live && l?.edition) setEdition(l.edition) })
      .catch(() => {})
    return () => { live = false }
  }, [trusted, open, edition])

  const latest = info?.newer && info.latest ? info.latest : null
  const seenKey = latest ? SEEN_KEY_PREFIX + latest : null
  useEffect(() => {
    if (!seenKey) return
    try {
      setSeen(localStorage.getItem(seenKey) === "1")
    } catch {
      setSeen(false)
    }
  }, [seenKey])

  const notesRef = useRef<HTMLAnchorElement>(null)
  const markSeen = () => {
    if (seenKey) {
      try { localStorage.setItem(seenKey, "1") } catch { /* storage may be unavailable */ }
    }
    setSeen(true)
    // The button goes away with the dot; keep the keyboard in the panel.
    requestAnimationFrame(() => notesRef.current?.focus())
  }

  const dot = Boolean(latest) && !seen
  const ariaLabel = latest ? `Crewship: version ${latest} is available` : "Crewship"

  return (
    <>
      <BarMenu
        icon={dot ? SailWithDot : Sail}
        ariaLabel={ariaLabel}
        open={open}
        onOpenChange={setOpen}
        testId="crewship"
        widthClass="w-[320px] max-md:fixed max-md:inset-x-2 max-md:top-12 max-md:w-auto"
      >
        <div className="flex items-center gap-3 px-3 pb-2.5 pt-3">
          <CrewshipLogoTile size="h-9 w-9" rounded="rounded-xl" iconSize="h-[18px] w-[18px]" />
          <div className="min-w-0">
            <p className="type-row font-semibold text-foreground">Crewship</p>
            {info && (
              <p className="type-meta truncate font-mono text-muted-foreground" data-testid="crewship-build">
                {edition ? `${editionName(edition)} · ` : ""}{buildLabel(info)}
              </p>
            )}
          </div>
        </div>

        {latest && (
          <div
            className="mx-2.5 mb-2.5 rounded-lg border border-primary/30 bg-primary/10 p-3"
            data-testid="crewship-update"
            data-seen={seen ? "true" : undefined}
          >
            <p className="type-row flex items-center gap-2 font-semibold text-foreground">
              {!seen && <span className="size-1.5 shrink-0 rounded-full bg-primary" aria-hidden />}
              New version available
            </p>
            <p className="type-meta mt-1 text-muted-foreground">
              <span className="font-mono text-foreground">{latest}</span> is out. You have{" "}
              <span className="font-mono text-foreground">{info?.current}</span>.
            </p>
            <div className="mt-2.5 flex flex-wrap gap-2">
              <Button asChild size="sm">
                <a ref={notesRef} href={info?.url ?? RELEASES_URL} target="_blank" rel="noopener noreferrer">Release notes</a>
              </Button>
              {!seen && (
                <Button size="sm" variant="outline" onClick={markSeen}>Mark as seen</Button>
              )}
            </div>
          </div>
        )}

        <ul className="grid gap-px border-t border-foreground/[0.06] p-1.5">
          {LINKS.map(({ label, href, icon: Icon }) => (
            <li key={label}>
              <a href={href} target="_blank" rel="noopener noreferrer" className={ROW}>
                <Icon className="size-4 text-muted-foreground" aria-hidden />
                <span className="flex-1">{label}</span>
                <ExternalLink className="size-3.5 text-muted-foreground" aria-hidden />
              </a>
            </li>
          ))}
          <li>
            <button
              type="button"
              className={cn(ROW, "w-full text-left")}
              onClick={() => {
                setOpen(false)
                setAboutOpen(true)
              }}
            >
              <Info className="size-4 text-muted-foreground" aria-hidden />
              <span className="flex-1">About Crewship</span>
            </button>
          </li>
        </ul>
      </BarMenu>
      <AboutCrewship open={aboutOpen} onOpenChange={setAboutOpen} trusted={trusted} />
    </>
  )
}

const ROW =
  "flex min-h-9 coarse:min-h-[44px] items-center gap-2.5 rounded-md px-2 text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"

/** The lit tile, the same as it was in the rail. */
function Sail() {
  return (
    <span className="bar-brand" aria-hidden data-testid="crewship-sail">
      <CrewshipLogo tight />
    </span>
  )
}

function SailWithDot() {
  return (
    <>
      <Sail />
      <span className="bar-brand-dot" aria-hidden data-testid="crewship-dot" />
    </>
  )
}

const LINKS = [
  { label: "Documentation", href: DOCS_URL, icon: BookOpen },
  { label: "What's new", href: RELEASES_URL, icon: Sparkles },
  { label: "Report a problem", href: SUPPORT_URL, icon: Bug },
]

export function AboutCrewship({ open, onOpenChange, trusted }: { open: boolean; onOpenChange: (open: boolean) => void; trusted: boolean }) {
  const [build, setBuild] = useState<Build | null>(null)
  const [edition, setEdition] = useState<string | undefined>()
  useEffect(() => {
    // A restricted session reads neither: both are off its allowlist, so
    // About shows what Crewship is and the links, without the build.
    if (!trusted || !open || build) return
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
  }, [trusted, open, build])

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
