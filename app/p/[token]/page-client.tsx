"use client"

import { useUrlSegment } from "@/lib/use-url-segment"
import { usePublicPage } from "@/components/features/pages/public/use-public-page"
import { PublicPageView } from "@/components/features/pages/public/public-page-view"

/**
 * The token comes from the URL, never from `useParams()`.
 *
 * Under `output: "export"` this route is exported once, as `/p/_.html`, and the
 * Go binary serves that file for every real token — so `useParams()` hands back
 * the literal "_" placeholder. `useUrlSegment` reads `window.location` after
 * mount and is the repo's existing answer to exactly this bug class; the issue,
 * skill, mission, chat and page detail routes all had it first. Here it would
 * not merely show the wrong page: every visitor would be asking the API for a
 * token called "_".
 */
const PUBLIC_PAGE_PATH_RE = /^\/p\/([^/]+)\/?$/

export function PublicPageClient() {
  const token = useUrlSegment(PUBLIC_PAGE_PATH_RE)
  const { status, page, message, submitting, submit } = usePublicPage(token)

  return (
    <PublicPageView
      status={token === null ? "loading" : status}
      page={page}
      message={message}
      submitting={submitting}
      onSubmitPassword={submit}
    />
  )
}
