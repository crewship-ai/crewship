"use client"

import { LegacyRedirect } from "../legacy-redirect"

const TARGET = "/activity?mode=spend"

// The Paymaster page became Activity's Spend view (#3618). It was a server
// redirect() in (dashboard), which the static export never runs (#3361).
export default function LegacyPaymasterRedirect() {
  return <LegacyRedirect target={TARGET}>Spend moved to Activity: <a href={TARGET}>open Spend</a>.</LegacyRedirect>
}
