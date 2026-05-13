"use client"

import { useEffect } from "react"
import { useParams, useRouter } from "next/navigation"

// Client redirect for the deprecated /orchestration/issues/[id] route.
// Split out of page.tsx because Next forbids "use client" alongside
// `generateStaticParams` in the same file.
export function OrchestrationIssueRedirect() {
  const router = useRouter()
  const params = useParams()
  useEffect(() => {
    const id = params?.identifier
    if (typeof id === "string" && id.length > 0 && id !== "_") {
      router.replace(`/issues/${id}`)
    } else {
      router.replace("/issues")
    }
  }, [router, params])
  return null
}
