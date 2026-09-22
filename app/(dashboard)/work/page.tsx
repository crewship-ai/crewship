"use client"

import { Suspense, useEffect } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"

// Preserve bookmarks to the former ledger route. Use replace so Back does not
// bounce through this alias. Suspense is required by the static export.
function WorkRedirect() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const params = new URLSearchParams(searchParams.toString())
  params.set("section", params.get("section") === "deliveries" ? "deliveries" : "work")
  const target = `/activity?${params.toString()}`

  useEffect(() => { router.replace(target, { scroll: false }) }, [router, target])

  return <p className="p-4 text-sm">Work is now in <Link href={target} className="underline">Activity</Link>.</p>
}

export default function WorkPage() {
  return <Suspense fallback={null}><WorkRedirect /></Suspense>
}
