"use client"

import Link from "next/link"
import { PauseCircle, Settings as SettingsIcon, Webhook } from "lucide-react"

import { DrillPage } from "@/components/layout/drill-page"
import { EmptyState } from "@/components/layout/empty-state"
import { Button } from "@/components/ui/button"

/**
 * What /settings/hooks shows while lifecycle hooks are paused
 * (internal/hooks/paused.go). Settings no longer links here; an old link or
 * bookmark lands on a plain statement — the kit's empty state, with the way
 * back as its one action — instead of a page whose every call would be
 * refused. HooksPage stays in the tree, unrouted, as the work in progress.
 */
export function HooksPaused() {
  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Lifecycle hooks"
      icon={Webhook}
      description="Paused"
      head={false}
    >
      <section aria-label="Lifecycle hooks are paused" className="mx-auto w-full max-w-xl p-4 md:p-6">
        <EmptyState
          icon={PauseCircle}
          title="Lifecycle hooks are paused"
          description="The feature is unfinished, so it is switched off — here, in the CLI and on the server. Hooks registered earlier are kept, but they do not run. It comes back once hooks can reach tools like Slack or Teams directly."
        >
          <Button asChild size="sm" className="mt-4">
            <Link href="/settings">Back to Settings</Link>
          </Button>
        </EmptyState>
      </section>
    </DrillPage>
  )
}
