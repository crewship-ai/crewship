"use client"

import { Suspense, useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"

import { useAbilities } from "@/hooks/use-abilities"
import { useJournalLookup } from "@/hooks/use-journal-lookup"
import { canSeeJournalViews, journalToActivity, lookupResolvers, needsLookup } from "@/lib/journal-redirect"

// /journal — merged into Activity (F-02). The page is a redirect now: every
// old link (bookmarks, Slack posts, `crewship open journal`, entityHref) lands
// on the equivalent Activity view with the same filters. OWNER / ADMIN keep
// the Journal's three views (technical log, fleet runs, spend) inside
// Activity; everyone else lands on the Activity list — the Journal was never
// theirs, and Spend stays out of their reach. The mapping is
// lib/journal-redirect.ts.
//
// Client-side because the target depends on the role, and the app is
// statically exported. `replace`, so Back does not return to a page that
// would only send the reader forward again.

function JournalRedirect() {
  const router = useRouter()
  const search = useSearchParams().toString()
  const { role, loading } = useAbilities()
  const lookup = useJournalLookup()
  const admin = canSeeJournalViews(role)

  useEffect(() => {
    if (loading) return
    const params = new URLSearchParams(search)
    // A member's crew / agent / issue arrives as a slug or an identifier; the
    // list filters on ids. Wait for the lookup rather than guessing.
    if (!admin && needsLookup(params) && lookup.loading) return
    router.replace(journalToActivity(params, { admin, pathname: "/journal", resolve: lookupResolvers(lookup) }))
  }, [loading, admin, search, lookup, router])

  return null
}

export default function JournalPage() {
  return (
    <Suspense fallback={null}>
      <JournalRedirect />
    </Suspense>
  )
}
