"use client"

import { Suspense } from "react"
import { useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { ArtifactsRoute } from "@/components/features/artifacts-library/artifacts-route"

// /artifacts — the Artifacts library. One open artifact has its own address,
// /artifacts/<id>; a link from before that, /artifacts?item=<slug>, opens by slug
// and moves to the ID address once the slug resolves.
export default function ArtifactsPage() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><ArtifactsEntry /></Suspense>
}

function ArtifactsEntry() {
  const params = useSearchParams()
  // A shared Design version opens full screen at /artifacts?design=1&item=<slug>; only other ?item= links are from before.
  const legacy = params?.get("design") === "1" ? null : params?.get("item") || null
  return <ArtifactsRoute item={legacy} bySlug={legacy !== null} />
}
