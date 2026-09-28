"use client"

import { useState } from "react"
import { MotionConfig } from "motion/react"
import { ThemeProvider } from "next-themes"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { AuthProvider } from "@/hooks/use-auth"
import { Toaster } from "@/components/ui/sonner"

/**
 * Root client-side providers. A single React Query client is held for
 * the lifetime of the app shell; list views prefer staleTime 30s so
 * polling does not hammer the backend while the user navigates tabs.
 * React Query is the canonical data layer for new and migrated
 * surfaces (dashboard, inbox, admin backups so far) — see the
 * "Frontend data fetching" section in CONTRIBUTING.md and
 * hooks/use-dashboard-data.ts for the conventions. Remaining
 * fetch+useState hooks migrate incrementally; a wholesale rewrite is
 * out of scope.
 */
export const HARBOR_THEMES = ["light", "dusk", "dark"]

export function Providers({ children }: { children: React.ReactNode }) {
  // useState keeps the client stable across re-renders. Creating a
  // fresh QueryClient on every render would dump the cache on every
  // parent state change.
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 30_000,
            refetchOnWindowFocus: false,
          },
        },
      }),
  )
  return (
    // Harbor themes: "dark" is Night (the default, unchanged for everyone who
    // never opens the switcher), "dusk" the violet-navy dark variant, "light"
    // Day. The class lands on <html>, where globals.css keys the palettes.
    <ThemeProvider attribute="class" defaultTheme="dark" themes={HARBOR_THEMES} enableSystem disableTransitionOnChange>
    <QueryClientProvider client={queryClient}>
      <MotionConfig reducedMotion="user">
        <AuthProvider>
          {children}
          <Toaster />
        </AuthProvider>
      </MotionConfig>
    </QueryClientProvider>
    </ThemeProvider>
  )
}
