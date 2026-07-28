"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { ArrowRight, Bell } from "lucide-react"
import { Button } from "@/components/ui/button"

/**
 * Notification channels and the preference matrix now live on the
 * Integrations page.
 *
 * This redirects rather than rendering the sections in a second place. Two
 * surfaces for the same object is worse than one inconvenient surface: they
 * drift, and an admin auditing "what is this instance wired into" has to
 * check both. The visible fallback exists because a redirect that fires
 * before hydration leaves a blank panel, and because a link a user followed
 * deliberately deserves an explanation rather than a silent jump.
 */
export function MovedToIntegrations() {
  const router = useRouter()

  useEffect(() => {
    router.replace("/integrations")
  }, [router])

  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="mb-3 flex h-9 w-9 items-center justify-center rounded-lg bg-muted/50">
        <Bell className="h-4 w-4 text-muted-foreground" />
      </div>
      <div className="text-xs font-medium text-foreground/80">
        Notifications moved to Integrations
      </div>
      <p className="mt-1 max-w-sm text-[11px] text-muted-foreground">
        Channels, your preference matrix, and the workspace-wide view of every
        connection now live together on one page.
      </p>
      <Button
        size="sm"
        className="mt-4 h-7 px-2.5 text-xs"
        onClick={() => router.replace("/integrations")}
      >
        Go to Integrations
        <ArrowRight className="ml-1.5 size-3" />
      </Button>
    </div>
  )
}
