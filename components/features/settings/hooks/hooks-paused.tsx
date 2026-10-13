"use client"

import Link from "next/link"
import { PauseCircle, Settings as SettingsIcon, Webhook } from "lucide-react"

import { DrillPage } from "@/components/layout/drill-page"

/**
 * What /settings/hooks shows while lifecycle hooks are paused
 * (internal/hooks/paused.go). Settings no longer links here; an old link or
 * bookmark lands on a plain statement instead of a page whose every call
 * would be refused. HooksPage stays in the tree, unrouted, as the work in
 * progress.
 */
export function HooksPaused() {
  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Lifecycle hooks"
      icon={Webhook}
      description="Paused"
    >
      <div className="p-4 md:p-6">
        <section aria-label="Lifecycle hooks are paused" className="mx-auto max-w-lg rounded-card border border-border bg-card p-6 text-center">
          <span className="mx-auto mb-3 grid h-12 w-12 place-items-center rounded-xl bg-surface-subtle text-muted-foreground"><PauseCircle className="h-6 w-6" aria-hidden /></span>
          <h2 className="text-base font-semibold">Lifecycle hooks are paused</h2>
          <p className="mx-auto mt-1.5 text-xs leading-relaxed text-muted-foreground">
            The feature is unfinished, so it is switched off — here, in the CLI and on the server. Hooks registered earlier are
            kept, but they do not run. It comes back once hooks can reach tools like Slack or Teams directly.
          </p>
          <Link href="/settings" className="mt-4 inline-block text-xs text-primary-hover hover:underline">Back to Settings</Link>
        </section>
      </div>
    </DrillPage>
  )
}
