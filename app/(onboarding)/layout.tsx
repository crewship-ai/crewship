"use client"

import { useEffect } from "react"
import { useSession } from "@/hooks/use-auth"
import { useRouter } from "next/navigation"
import { SetupCardSkeleton } from "@/components/features/onboarding/setup-card"

export default function OnboardingLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const { status } = useSession()
  const router = useRouter()

  useEffect(() => {
    if (status === "unauthenticated") {
      router.push("/login")
    }
  }, [status, router])

  // The same skeleton the page shows while it restores the setup, so the
  // session check and the restore read as one load, not two spinners.
  if (status === "loading" || status === "unauthenticated") {
    return <SetupCardSkeleton />
  }

  return <>{children}</>
}
