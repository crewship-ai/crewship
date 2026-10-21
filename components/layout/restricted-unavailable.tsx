"use client"

import { Lock } from "lucide-react"

import { PageNoAccess } from "@/components/layout/page-state"
import { SubBar } from "@/components/layout/sub-bar"
import { areaOf } from "@/lib/nav-sections"
import { RESTRICTED_SURFACE_PATHS, restrictedHome } from "@/lib/restricted-surfaces"
import { useRestrictedSurfaces } from "@/hooks/use-access-mode"

/** The name a person knows a screen by: the navigation's label (Vault, not
 *  "Credentials"), else the path's first segment made readable. */
export function screenName(pathname: string | undefined): string {
  if (!pathname) return "This screen"
  const area = areaOf(pathname)
  if (area && area.href !== "/") return area.title
  if (pathname === "/") return "Dashboard"
  const screen = pathname.split("/").filter(Boolean)[0]
  return screen ? screen.charAt(0).toUpperCase() + screen.slice(1).replace(/-/g, " ") : "This screen"
}

/**
 * What a restricted session sees at a screen its access does not include
 * (#2861): a direct URL, an old bookmark, a link someone shared. The page
 * itself never mounts, so none of its requests are sent.
 *
 * Harbor 2 G03 — the No-access state in its restricted variant: the screen's
 * name, one sentence and one way on. No admin names and no "Ask an admin":
 * telling a guest who runs the workspace, or that a role would open this, is
 * the structure the restricted allowlist exists to keep private.
 */
export function RestrictedUnavailable({ pathname, home }: {
  /** Where "Go to …" leads. Defaults to the session's restricted home. */
  home?: string | null
  /** The screen that was asked for. */
  pathname?: string
}) {
  const surfaces = useRestrictedSurfaces() ?? []
  const name = screenName(pathname)
  const target = home ?? restrictedHome(surfaces)
  const targetName = target ? screenName(target) : null
  const allowed = target != null && Object.values(RESTRICTED_SURFACE_PATHS).includes(target) && target !== pathname
  return (
    <div role="status" className="flex min-h-full flex-col bg-background">
      <SubBar icon={Lock} title={name} description="Outside your access" ariaLabel={name} />
      <PageNoAccess
        area={name}
        title={`${name} isn’t part of your access`}
        description="You can open what was shared with you in this workspace."
        primary={allowed && targetName ? { label: `Go to ${targetName}`, href: target } : undefined}
      />
    </div>
  )
}
