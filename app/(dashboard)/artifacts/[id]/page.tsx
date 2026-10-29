import { Suspense } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { ArtifactDetailRoute } from "./page-client"

// Under `output: "export"` a dynamic route declares its params at build time.
// The single `_` param is exported as /artifacts/_.html; the Go binary serves
// it for every real ID and the client reads the actual one from useParams,
// the same split /pages/[slug] uses.
export function generateStaticParams() {
  return [{ id: "_" }]
}

export default function ArtifactRoute() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><ArtifactDetailRoute /></Suspense>
}
