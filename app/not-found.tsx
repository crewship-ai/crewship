"use client"

import DashboardLayout from "@/app/(dashboard)/layout"
import { AppFrame } from "@/components/layout/app-frame"
import { BareNotFound, NotFoundInShell } from "@/components/layout/not-found-state"
import { useAuthAvailable, useSessionSafe } from "@/hooks/use-auth"

/**
 * Unknown route (Harbor 2 G05) under `next dev`/`next start`, and the
 * exported 404.html. On the static-export deployment the Go backend serves
 * the root index.html for an unknown path instead, and the dashboard layout
 * shows the same states itself (components/layout/not-found-state.tsx).
 *
 * Signed in, it renders inside the app shell — rail, top bar, Search — so the
 * person is not thrown out of the app by an old link. Signed out (or outside
 * the auth provider), it is the bare state on the ground with the lockup.
 */
export default function NotFound() {
  const authAvailable = useAuthAvailable()
  const { status } = useSessionSafe()
  if (authAvailable && status === "loading") return <AppFrame />
  if (authAvailable && status === "authenticated") {
    return (
      <DashboardLayout>
        <NotFoundInShell />
      </DashboardLayout>
    )
  }
  return <BareNotFound />
}
