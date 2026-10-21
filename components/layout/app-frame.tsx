"use client"

import * as React from "react"
import { LogOut } from "lucide-react"

import { Button } from "@/components/ui/button"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { PageError, QUIET_LOAD_MS, useElapsed } from "@/components/layout/page-state"

/**
 * The app's frame before there is anything to put in it (Harbor 2 G01/G02):
 * the rail, the top bar and — on a phone — the tab bar, drawn as skeletons.
 * It replaces the full-page spinner the shell showed while the session and the
 * workspace list loaded: under a second it is just the ground, then the frame
 * in the shape the app is about to take.
 *
 * `error` keeps the frame and puts the error card in the content. Log out stays
 * reachable there, because a person whose workspaces cannot load otherwise has
 * no way out of the app.
 */
export function AppFrame({
  state = "loading",
  onRetry,
  onSignOut,
  delay = QUIET_LOAD_MS,
  children,
}: {
  state?: "loading" | "error"
  onRetry?: () => void
  onSignOut?: () => void
  /** Milliseconds of plain ground before the skeleton frame shows. */
  delay?: number
  /** Content for the frame's body; the error card when `state="error"`. */
  children?: React.ReactNode
}) {
  // An error shows at once, also when the same frame was still in its quiet
  // second of loading (the hook keeps its state across the switch).
  const elapsed = useElapsed(delay)
  const shown = state === "error" || elapsed
  if (!shown) {
    return (
      <div className="app-frame" role="status" aria-busy="true" data-state="app-loading" data-quiet="">
        <span className="sr-only">Loading</span>
      </div>
    )
  }
  const loading = state === "loading"
  return (
    <div
      className="app-frame"
      role={loading ? "status" : undefined}
      aria-busy={loading || undefined}
      data-state={loading ? "app-loading" : "app-error"}
    >
      {loading && <span className="sr-only">Loading</span>}
      <aside className="app-frame-side" aria-hidden>
        <div className="app-frame-ws">
          <span className="page-sk app-frame-tile" />
          <span className="page-sk app-frame-label" />
        </div>
        {/* Before the workspace is known there are no items to draw; the rows
            are the shape of the navigation, not the navigation. */}
        {loading && Array.from({ length: 8 }, (_, i) => <span key={i} className="page-sk app-frame-row" />)}
      </aside>
      <div className="app-frame-main">
        <div className="app-frame-top">
          <span className="page-sk app-frame-crumb" aria-hidden />
          <span className="flex-1" />
          {onSignOut && (
            <Button variant="ghost" size="sm" onClick={onSignOut} className="coarse:h-[44px]">
              <LogOut className="size-3.5" aria-hidden />
              Log out
            </Button>
          )}
        </div>
        <div className="app-frame-body">
          {state === "error"
            ? children ?? (
                <PageError
                  title="Your workspaces could not load"
                  description="The server did not answer. Nothing you saved is lost."
                  onRetry={onRetry}
                />
              )
            : children}
        </div>
        <div className="app-frame-tabs" aria-hidden>
          {Array.from({ length: 5 }, (_, i) => <span key={i} className="page-sk app-frame-tab" />)}
        </div>
      </div>
    </div>
  )
}

/**
 * A state shown outside the app frame — a signed-out 404, a crashed root —
 * on the plain ground with the Crewship lockup above it.
 */
export function BareState({ children }: { children: React.ReactNode }) {
  return (
    <main className="bare-state">
      <span className="bare-state-lockup">
        <CrewshipLogoTile size="h-8 w-8" rounded="rounded-lg" iconSize="h-4 w-4" />
        Crewship
      </span>
      {children}
    </main>
  )
}
