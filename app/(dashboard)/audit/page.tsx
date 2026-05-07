import { redirect } from "next/navigation"

// Audit Log moved to Settings (admin compliance section). Old bookmarks
// land here and bounce to /settings?tab=audit so any link posted on
// Slack / docs / dashboards keeps resolving.
//
// Server-side redirect (App Router) so it happens before any client JS
// hydrates, avoiding a flash of empty layout.
export default function AuditRedirect(): never {
  redirect("/settings?tab=audit")
}
