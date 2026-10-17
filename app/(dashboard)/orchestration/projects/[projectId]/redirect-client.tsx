"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { useUrlSegment } from "@/lib/use-url-segment"

// Read the id from the URL, not useParams(): in the static export
// useParams() returns the "_" placeholder (see useUrlSegment).
const ORCH_PROJECT_RE = /^\/orchestration\/projects\/([^/]+)\/?$/

// Projects live on /issues?project=<id>. The id used to be dropped here, so
// a bookmarked project opened the whole issue list instead.
export function OrchestrationProjectRedirect() {
  const router = useRouter()
  const id = useUrlSegment(ORCH_PROJECT_RE)
  useEffect(() => {
    if (id === null) return // not mounted yet
    if (id && id !== "_") router.replace(`/issues?${new URLSearchParams({ project: id })}`)
    else router.replace("/issues")
  }, [router, id])
  return null
}
