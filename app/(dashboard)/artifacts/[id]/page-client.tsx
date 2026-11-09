"use client"

import { useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { useUrlSegment } from "@/lib/use-url-segment"
import { ArtifactsRoute } from "@/components/features/artifacts-library/artifacts-route"

// The static export serves artifacts/_.html for every ID and useParams() keeps
// returning that "_" placeholder, so the ID is read from the live URL.
const ID = /^\/artifacts\/([^/]+)\/?$/
/** `@<id>`, the form the CLI and `/pages/@<id>` use. `@` is never part of an ID or a slug. */
const AT_ID = /^@([A-Za-z0-9_-]{1,128})$/

/** /artifacts/<id>: the library with that artifact open. The exported `_` shell alone is the library. */
export function ArtifactDetailRoute() {
  const id = useUrlSegment(ID)
  const params = useSearchParams()
  const router = useRouter()
  // Moves by address alone, before any read, so it answers the same for every ID.
  const atId = id === null ? null : AT_ID.exec(id)?.[1] ?? null
  useEffect(() => {
    if (atId) router.replace(`/artifacts/${encodeURIComponent(atId)}${params?.size ? `?${params}` : ""}`)
  }, [atId]) // eslint-disable-line react-hooks/exhaustive-deps
  if (id === null || atId) return <Skeleton className="m-6 h-12" />
  return <ArtifactsRoute item={id !== "_" ? id : null} />
}
