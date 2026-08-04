"use client"

// /routines-new — unlisted design preview for the routine detail
// redesign. Not linked from the sidebar; reachable only by typing the
// URL, which is what we want while the layout is still an argument.
//
// It needs no workspace: every variant renders from static fixtures in
// lib/routines-preview, so it paints identically on a fresh instance
// and cannot be broken by seed data.

import { RoutinesNewPreview } from "@/components/features/routines-new/routines-new-preview"

export default function RoutinesNewPage() {
  return <RoutinesNewPreview />
}
