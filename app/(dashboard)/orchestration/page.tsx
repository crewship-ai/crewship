"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

// /orchestration — soft-redirect alias kept for ~1 release window so
// bookmarks and external docs that point at the old URL keep working.
// The page was split into /issues + /activity + /routines as part of
// the Plan/Run/Build/System IA refactor.
export default function OrchestrationRedirect() {
  const router = useRouter()
  useEffect(() => {
    router.replace("/activity")
  }, [router])
  return null
}
