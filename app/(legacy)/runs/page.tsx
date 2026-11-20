"use client"

import { LegacyRedirect } from "../legacy-redirect"

const TARGET = "/activity?section=runs"

// /runs was folded into the Journal's Runs tab, and the Journal into Activity
// (F-02). Old bookmarks land on the fleet runs ledger; Activity sends anyone
// below ADMIN on to its own list of runs (`?what=work`) — see
// lib/journal-redirect. It was a server redirect() in (dashboard), which the
// static export never runs (#3361).
export default function LegacyRunsRedirect() {
  return <LegacyRedirect target={TARGET}>Runs moved to Activity: <a href={TARGET}>open runs</a>.</LegacyRedirect>
}
