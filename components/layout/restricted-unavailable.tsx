"use client"

import { Lock } from "lucide-react"

import { AccessNotice, useAccessContext } from "@/components/features/restricted/access-notice"
import { SubBar } from "@/components/layout/sub-bar"

/**
 * What a restricted session sees at a screen its access does not include
 * (#2861): a direct URL, an old bookmark, a link someone shared. The page
 * itself never mounts, so none of its requests are sent.
 */
export function RestrictedUnavailable({ pathname }: {
  /** Kept for callers that still pass it; the notice links to the screens
   *  the server allows instead. */
  home?: string | null
  /** The screen that was asked for, named in the sub-bar. */
  pathname?: string
}) {
  const access = useAccessContext()
  const screen = pathname?.split("/").filter(Boolean)[0]
  const name = screen ? screen.charAt(0).toUpperCase() + screen.slice(1).replace(/-/g, " ") : "This screen"
  return (
    <div role="status" className="flex min-h-full flex-col bg-background">
      <SubBar icon={Lock} title={name} description="Outside your access" ariaLabel={name} />
      <div className="p-4">
        <AccessNotice
          kind="no-access"
          title="Not available with your access"
          description="This screen is not part of your access in this workspace."
          context={access}
        />
      </div>
    </div>
  )
}
