"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { useUrlSegment } from "@/lib/use-url-segment"

// Read the identifier from the URL, not useParams() — in the static export
// useParams() returns the "_" placeholder, which would bounce every
// bookmarked /orchestration/issues/<id> to the issues list instead of the
// specific issue. See useUrlSegment.
const ORCH_ISSUE_RE = /^\/orchestration\/issues\/([^/]+)\/?$/

// Client redirect for the deprecated /orchestration/issues/[id] route.
// Split out of page.tsx because Next forbids "use client" alongside
// `generateStaticParams` in the same file.
export function OrchestrationIssueRedirect() {
  const router = useRouter()
  const id = useUrlSegment(ORCH_ISSUE_RE)
  useEffect(() => {
    if (id === null) return // not mounted yet
    if (id && id !== "_") {
      router.replace(`/issues/${encodeURIComponent(id)}`)
    } else {
      router.replace("/issues")
    }
  }, [router, id])
  return null
}
