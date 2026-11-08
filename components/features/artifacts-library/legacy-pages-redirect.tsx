"use client"

import { useEffect, useState, type ReactNode } from "react"
import { useRouter } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { useSessionSafe } from "@/hooks/use-auth"
import { usePage } from "@/hooks/use-pages"
import { canonicalArtifactHref, resolvedArtifactId, type LegacyPagesTarget } from "./legacy-pages-url"

/**
 * Resolves an old Pages address once, with the same page read Pages itself
 * makes, and replaces the entry with /artifacts/<id> (the static export has no
 * server redirect; replace, so Back does not return here). Fails closed: a
 * refused or failed read, no session or no usable ID renders `children`, the
 * Pages view the address always opened. The entry is frozen at mount, so
 * later navigation inside Pages never redirects an open editor away.
 */
export function LegacyPagesRedirect({ target, children }: { target: Extract<LegacyPagesTarget, { to: "resolve" }>; children: ReactNode }) {
  const [stay, setStay] = useState(false)
  if (stay) return <>{children}</>
  // Only the address is frozen: a workspace switch while the read is pending
  // starts the decision again in the workspace now selected.
  return <Resolve key={target.workspaceId} target={target} onStay={() => setStay(true)} />
}

function Resolve({ target: entry, onStay }: { target: Extract<LegacyPagesTarget, { to: "resolve" }>; onStay: () => void }) {
  const router = useRouter()
  const session = useSessionSafe()
  const [target] = useState(entry)
  const detail = usePage(target.workspaceId, target.reference)
  const signedOut = session.status !== "loading" && !session.data?.user?.id
  const settled = signedOut || Boolean(detail.error || detail.accessDenied) || !detail.loading
  const id = settled && !signedOut && !detail.error && !detail.accessDenied ? resolvedArtifactId(target, detail.page) : null
  useEffect(() => {
    if (!settled) return
    if (id) router.replace(canonicalArtifactHref(id, target.workspaceId, target.version))
    else onStay()
  }, [settled, id]) // eslint-disable-line react-hooks/exhaustive-deps
  return <LegacyPagesSkeleton />
}

export function LegacyPagesSkeleton() {
  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col gap-3 p-4">
      <Skeleton className="h-9 w-full" />
      <div className="flex flex-1 gap-3">
        <Skeleton className="h-full w-60" />
        <Skeleton className="h-full flex-1" />
      </div>
    </div>
  )
}
