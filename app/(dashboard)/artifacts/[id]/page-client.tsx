"use client"

import { Skeleton } from "@/components/ui/skeleton"
import { useUrlSegment } from "@/lib/use-url-segment"
import { ArtifactsRoute } from "@/components/features/artifacts-library/artifacts-route"

// The static export serves artifacts/_.html for every ID and useParams() keeps
// returning that "_" placeholder, so the ID is read from the live URL.
const ID = /^\/artifacts\/([^/]+)\/?$/

/** /artifacts/<id>: the library with that artifact open. The exported `_` shell alone is the library. */
export function ArtifactDetailRoute() {
  const id = useUrlSegment(ID)
  if (id === null) return <Skeleton className="m-6 h-12" />
  return <ArtifactsRoute item={id !== "_" ? id : null} />
}
