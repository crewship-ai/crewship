"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { restrictedArtifactsHref } from "./restricted-artifacts-url"

/**
 * A restricted member at an old /pages address (#5132): replace the entry
 * with the matching /artifacts address and render nothing else. It sends no
 * request; the address is read from `window.location` because the static
 * export serves /pages/_ for every slug.
 */
export function RestrictedPagesRedirect({ before }: { before?: () => void }) {
  const router = useRouter()
  useEffect(() => {
    before?.()
    router.replace(restrictedArtifactsHref(window.location.pathname, window.location.search.replace(/^\?/, "")))
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <div className="flex flex-col gap-3 p-4"><Skeleton className="h-9 w-full" /><Skeleton className="h-40 w-full" /></div>
}
