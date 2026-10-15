"use client"

import { useAccessMode } from "@/hooks/use-access-mode"
import { RestrictedIssues } from "@/components/features/issues/restricted-issues"
import { Suspense } from "react"
import { OrchestrationPageShell } from "@/components/features/orchestration/orchestration-page-shell"

// /issues — top-level Issues surface. Carved out of the legacy
// /orchestration container in the IA refactor (Plan/Run/Build/System).
// Renders only the issues board+list; tab bar is suppressed because
// Graph/Timeline/Feed now live on /activity, and Routines lives on
// /routines as a top-level page in its own right.
export default function IssuesPage() {
  const mode = useAccessMode()
  if (mode === "loading") return null
  if (mode === "restricted") return <RestrictedIssues />
  return <Suspense><OrchestrationPageShell mode="issues" /></Suspense>
}
