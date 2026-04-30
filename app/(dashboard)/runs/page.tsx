import { redirect } from "next/navigation"

// Phase F of unified-journal: /runs has been folded into /journal as a
// preset tab. Old bookmarks land here and are bounced over so links
// posted on Slack / docs / dashboards don't 404.
//
// The redirect is server-side (App Router redirect()) so it happens
// before any client JS hydrates, avoiding a flash of empty layout.
export default function RunsRedirect(): never {
  redirect("/journal?tab=runs")
}
