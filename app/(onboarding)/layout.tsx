"use client"

import { useEffect, useRef } from "react"
import { useSession } from "@/hooks/use-auth"
import { useRouter } from "next/navigation"
import { PageLoading, QUIET_LOAD_MS } from "@/components/layout/page-state"
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

  // Harbor 2: no full-page spinner. Onboarding has no app rail, so the wait
  // is the page skeleton on the ground: nothing for a second, then the shape.
  if (status === "loading" || status === "unauthenticated") {
    return (
      <div className="min-h-dvh bg-background">
        <PageLoading delay={QUIET_LOAD_MS} rows={4} />
      </div>
    )
  }

  return <>{children}</>
}
