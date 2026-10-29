"use client"

import { Suspense, useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { ArtifactsRoute, artifactHref } from "@/components/features/artifacts-library/artifacts-route"

// /artifacts — the Artifacts library. One open artifact has its own address,
// /artifacts/<id>; a link from before that, /artifacts?item=<slug>, moves there.
export default function ArtifactsPage() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><ArtifactsEntry /></Suspense>
}

function ArtifactsEntry() {
  const params = useSearchParams()
  const router = useRouter()
  const legacy = params?.get("item") || null
  useEffect(() => {
    if (!legacy) return
    const rest = new URLSearchParams(params?.toString())
    rest.delete("item")
    router.replace(artifactHref(legacy, rest))
  }, [legacy]) // eslint-disable-line react-hooks/exhaustive-deps
  if (legacy) return <Skeleton className="m-6 h-12" />
  return <ArtifactsRoute item={null} />
}
