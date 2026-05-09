"use client"

import { OrchestrationPageShell } from "@/components/features/orchestration/orchestration-page-shell"

// /activity — live observability surface. Renders Graph (default),
// Timeline and Feed as sub-views of a single page; no top-level tabs
// for Issues or Routines because those are their own destinations now.
// Replaces /orchestration as the "what's running across the workspace
// right now" page.
export default function ActivityPage() {
  return <OrchestrationPageShell mode="activity" />
}
