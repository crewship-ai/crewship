import { redirect } from "next/navigation"

// /runs was folded into the Journal's Runs tab, and the Journal into Activity
// (F-02). Old bookmarks land on the fleet runs ledger; Activity sends anyone
// below ADMIN on to its own list of runs (`?what=work`), as the Journal gate
// did before — see lib/journal-redirect.
//
// The redirect is server-side (App Router redirect()) so it happens
// before any client JS hydrates, avoiding a flash of empty layout.
export default function RunsRedirect(): never {
  redirect("/activity?section=runs")
}
