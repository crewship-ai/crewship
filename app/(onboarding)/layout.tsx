"use client"

import { useEffect, useRef } from "react"
import { useSession } from "@/hooks/use-auth"
import { useRouter } from "next/navigation"
import { SetupCardSkeleton } from "@/components/features/onboarding/setup-card"
import { loginRedirectUrl } from "@/lib/login-redirect"

export default function OnboardingLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const { status } = useSession()
  const router = useRouter()
  const hadSession = useRef(false)
  if (status === "authenticated") hadSession.current = true

  useEffect(() => {
    if (status === "unauthenticated") {
      router.push(loginRedirectUrl(window.location.pathname, window.location.search, hadSession.current))
    }
  }, [status, router])

  // The same skeleton the page shows while it restores the setup, so the
  // session check and the restore read as one load, not two spinners.
  if (status === "loading" || status === "unauthenticated") {
    return <SetupCardSkeleton />
  }

  return <>{children}</>
}
