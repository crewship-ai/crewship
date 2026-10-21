"use client"

import { Suspense } from "react"
import { useRouter } from "next/navigation"
import { ArrowLeft, Inbox } from "lucide-react"

import { useAccessMode } from "@/hooks/use-access-mode"
import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"
import { useWorkspace } from "@/hooks/use-workspace"
import { PageLoading } from "@/components/layout/page-state"
import { SubBar, SubBarIconButton } from "@/components/layout/sub-bar"
import { RestrictedIssues } from "@/components/features/issues/restricted-issues"
import { IntakeSurface, type IntakeTab } from "@/components/features/issues/intake-surface"

// /issues/intake — Triage rules and Recurring issues (Harbor 2 Issues › Intake).
// A static segment, so it wins over /issues/[identifier]. The tab rides in
// ?tab= so a refresh or a pasted link lands on the same list.
function IntakeScreen() {
  const mode = useAccessMode()
  const router = useRouter()
  const { workspaceId, loading } = useWorkspace()
  const [tabParam, setTab] = useShallowSearchParam("tab")
  if (mode === "loading") return null
  if (mode === "restricted") return <RestrictedIssues />
  const tab: IntakeTab = tabParam === "recurring" ? "recurring" : "triage"

  return (
    <div className="flex h-full flex-col bg-background">
      <SubBar
        icon={Inbox}
        title="Issues"
        section="Intake"
        ariaLabel="Intake"
        leading={<SubBarIconButton icon={ArrowLeft} onClick={() => router.push("/issues")} aria-label="Back to issues" />}
      />
      <div className="min-h-0 flex-1 overflow-y-auto pb-20">
        {loading || !workspaceId ? (
          <PageLoading variant="list" rows={4} />
        ) : (
          <IntakeSurface workspaceId={workspaceId} tab={tab} onTabChange={setTab} />
        )}
      </div>
    </div>
  )
}

export default function IntakePage() {
  // useSearchParams needs a Suspense boundary in the static export.
  return (
    <Suspense>
      <IntakeScreen />
    </Suspense>
  )
}
