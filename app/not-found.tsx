"use client"

import { Search } from "lucide-react"

import DashboardLayout from "@/app/(dashboard)/layout"
import { AppFrame, BareState } from "@/components/layout/app-frame"
import { PageNotFound } from "@/components/layout/page-state"
import { SubBar } from "@/components/layout/sub-bar"
import { useAuthAvailable, useSessionSafe } from "@/hooks/use-auth"
import { useAppStore } from "@/lib/store"

const DESCRIPTION = "The link may be old, or the page was moved."

/** The 404 inside the app: the shell stays, the content says what happened. */
function NotFoundInShell() {
  const openPalette = useAppStore((s) => s.setCommandPaletteOpen)
  return (
    <>
      <SubBar icon={Search} title="Not found" ariaLabel="Not found" />
      <PageNotFound
        title="This page doesn’t exist"
        description={DESCRIPTION}
        primary={{ label: "Go to Dashboard", href: "/" }}
        secondary={{ label: "Search", icon: Search, onClick: () => openPalette(true) }}
      />
    </>
  )
}

/**
 * Unknown route (Harbor 2 G05). Signed in, it renders inside the app shell —
 * rail, top bar, Search — so the person is not thrown out of the app by an
 * old link. Signed out (or outside the auth provider), it is the bare state
 * on the ground with the lockup.
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
  return (
    <BareState>
      <PageNotFound
        title="This page doesn’t exist"
        description={DESCRIPTION}
        primary={{ label: "Go to Dashboard", href: "/" }}
      />
    </BareState>
  )
}
