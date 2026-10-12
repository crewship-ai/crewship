"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { entityHref } from "@/lib/entity-links"
import { useUrlSegment } from "@/lib/use-url-segment"

// /skills/<id> was a separate page with its own vocabulary. A skill now opens
// beside the explorer on /skills (#3033); old links and bookmarks land there.
// Read the id from the URL, not useParams() — see useUrlSegment.
const SKILL_PATH_RE = /^\/skills\/([^/]+)\/?$/

export function SkillDetailPageClient() {
  const router = useRouter()
  const skillId = useUrlSegment(SKILL_PATH_RE)
  useEffect(() => {
    if (skillId) router.replace(entityHref({ kind: "skill", id: skillId }))
  }, [skillId, router])
  return (
    <div className="p-5" aria-busy>
      <Skeleton className="h-28 rounded-card" />
    </div>
  )
}
