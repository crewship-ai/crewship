"use client"

import { useEffect, useState } from "react"
import Link from "next/link"

import { apiFetch } from "@/lib/api-fetch"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar"

interface Build {
  current: string
  commit?: string
  dirty?: boolean
  latest?: string | null
  newer?: boolean
}

const EDITIONS: Record<string, string> = { community: "Community Edition", team: "Team Edition", enterprise: "Enterprise Edition" }

/** "v0.9.2", or "dev · d43bfd1" for a build without a release tag. */
export function buildLabel(b: Build): string {
  if (b.current !== "dev" || !b.commit) return b.current
  return `dev · ${b.commit.slice(0, 7)}${b.dirty ? "+" : ""}`
}

export function editionName(edition: string | undefined): string {
  if (!edition) return "Crewship"
  return EDITIONS[edition] ?? `${edition.charAt(0).toUpperCase()}${edition.slice(1)} Edition`
}

/**
 * Which Crewship this is, at the foot of the app rail: the ship, the edition
 * and the build. Collapsed to icons, the ship alone stays and the tooltip says
 * the rest. An admin can click through to the Admin overview; for everyone
 * else it is a label.
 *
 * Both reads are cheap, cached server-side and fetched once per page load; a
 * failure leaves the row out rather than printing "unknown".
 */
export function SidebarVersion() {
  const instanceAdmin = useIsInstanceAdmin()
  const [build, setBuild] = useState<Build | null>(null)
  const [edition, setEdition] = useState<string | undefined>()

  useEffect(() => {
    let cancelled = false
    apiFetch("/api/v1/system/version")
      .then((r) => (r.ok ? r.json() : null))
      .then((b) => { if (!cancelled && b?.current) setBuild(b) })
      .catch(() => {})
    apiFetch("/api/v1/system/license")
      .then((r) => (r.ok ? r.json() : null))
      .then((l) => { if (!cancelled && l?.edition) setEdition(l.edition) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  if (!build) return null
  const name = editionName(edition)
  const label = buildLabel(build)
  const update = build.newer && build.latest ? build.latest : null
  const body = (
    <>
      <span className="icon-tile grid size-6 shrink-0 place-items-center rounded-md" aria-hidden>
        <CrewshipLogo tight className="h-3 w-auto" />
      </span>
      <span className="grid min-w-0 flex-1 leading-tight">
        <span className="truncate text-[12px] font-medium">{name}</span>
        <span className="truncate font-mono text-[10.5px] text-muted-foreground" data-slot="sidebar-build">
          {label}
          {update && <span className="ml-1.5 text-info">· {update} available</span>}
        </span>
      </span>
    </>
  )
  const tooltip = `Crewship ${name} · ${label}${update ? ` · ${update} available` : ""}`

  return (
    <SidebarMenuItem>
      {instanceAdmin ? (
        <SidebarMenuButton asChild size="lg" tooltip={tooltip} className="h-10">
          <Link href="/admin?tab=overview" aria-label={tooltip}>{body}</Link>
        </SidebarMenuButton>
      ) : (
        <SidebarMenuButton size="lg" tooltip={tooltip} className="h-10 cursor-default hover:bg-transparent" aria-label={tooltip}>
          {body}
        </SidebarMenuButton>
      )}
    </SidebarMenuItem>
  )
}
