"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import Link from "next/link"

/** Compatibility entry for saved links. The static export cannot use Next redirects. */
export default function LegacyInboxPage() {
  const router = useRouter()
  useEffect(() => {
    router.replace(`/inbox${window.location.search}${window.location.hash}`)
  }, [router])
  return <div className="p-6 text-body text-muted-foreground">Opening your inbox… <Link href="/inbox" className="text-primary-hover underline">Open Inbox</Link></div>
}
