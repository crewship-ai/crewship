import { PageDetailClient } from "./page-client"
import { Suspense } from "react"
import { Skeleton } from "@/components/ui/skeleton"

// A dynamic route under `output: "export"` must declare its params at build
// time, so the shell is a server component and the client half lives next
// door — the same split `issues/[identifier]` and `skills/[skillId]` use.
//
// The single `_` param is the placeholder Next.js exports as
// `/pages/_/index.html`; the Go binary serves it for every real slug and the
// client reads the actual one from `useParams`. Without this the whole
// production build fails, which is how it was caught: dev3 refused to come
// back up.
export function generateStaticParams() {
  return [{ slug: "_" }]
}

export default function PageDetailRoute() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><PageDetailClient /></Suspense>
}
