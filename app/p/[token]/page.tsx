import type { Metadata } from "next"

import { PublicPageClient } from "./page-client"

/**
 * `/p/{token}` — the public page (PRD `docs/prd/pages.md` §7.3.1).
 *
 * "A public page is served from a SEPARATE URL SPACE (/p/{token}) that shares
 * no session, no cookie and no workspace context with the app." That is why
 * this route sits at the top of `app/` and not under `(dashboard)`: the
 * dashboard group's layout mounts the sidebar, the workspace provider and the
 * realtime socket, none of which a reader with no account can use and all of
 * which would fire requests that 401.
 *
 * A dynamic route under `output: "export"` must declare its params at build
 * time, so the shell is a server component and the client half lives next
 * door — the same split `pages/[slug]` and `issues/[identifier]` use. The
 * single `_` param exports as `/p/_.html`; the Go binary's static handler
 * serves it for every real token and the client reads the actual one from the
 * live URL.
 */
export function generateStaticParams() {
  return [{ token: "_" }]
}

/**
 * §7.3.2 rule 6: "Not indexable". The API response carries `X-Robots-Tag:
 * noindex` and `Referrer-Policy: no-referrer`, but the HTML a crawler would
 * actually index is this file, served by the SPA static handler — so the same
 * two instructions are stated again as meta tags, which is the only mechanism
 * available to a statically exported document.
 *
 * The referrer tag is not a duplicate of the header, it is the stronger half:
 * a `<meta name="referrer">` governs requests the DOCUMENT makes, so a reader
 * who clicks a link out of a public page cannot hand the token — which IS the
 * credential, and which is in the URL — to a third-party site in a Referer
 * header.
 */
export const metadata: Metadata = {
  title: "Shared page",
  robots: {
    index: false,
    follow: false,
    nocache: true,
    googleBot: { index: false, follow: false },
  },
  referrer: "no-referrer",
}

export default function PublicPageRoute() {
  return <PublicPageClient />
}
