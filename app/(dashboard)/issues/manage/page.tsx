"use client"

import { Suspense, useEffect, useState } from "react"
import { useSearchParams } from "next/navigation"
import { useSession } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
import { encodeApiPathSegment } from "@/lib/api-path-segment"
import { KnownProjectDetails } from "@/components/features/issues/shared-project-details"

function KnownProjectManagement() {
  const params = useSearchParams()
  const { data: session } = useSession()
  const { workspaceId, workspaceUnavailable } = useWorkspace()
  const mode = useAccessMode()
  const project = params.get("project")
  const issue = params.get("issue_id")
  const valid = params.getAll("project").length === 1 && params.getAll("issue_id").length <= 1 && Array.from(params.keys()).every(key => key === "project" || key === "issue_id") && project !== null && project.length <= 128 && encodeApiPathSegment(project) !== null && (issue === null || (issue.length <= 128 && encodeApiPathSegment(issue) !== null))
  const scope = valid && workspaceId && session?.user.id ? JSON.stringify([session.user.id, workspaceId, project]) : null
  const issueScope = scope && issue ? JSON.stringify([scope, issue]) : null
  const [deletedScope, setDeletedScope] = useState<string | null>(null)
  const eligible = mode === "restricted" && scope !== null && !workspaceUnavailable
  const [mountedScope, setMountedScope] = useState<string | null>(null)
  useEffect(() => {
    if (eligible) setMountedScope(scope)
    else if (!workspaceUnavailable || mountedScope !== scope) setMountedScope(null)
  }, [eligible, scope, workspaceUnavailable, mountedScope])
  // Directory loss is not resource revocation. The surrounding availability
  // boundary hides and disables this existing draft; it cannot admit a new scope.
  const preserve = workspaceUnavailable && scope !== null && mountedScope === scope
  if (mode === "loading" && !preserve) return <p role="status">Checking project access…</p>
  return <section className="mx-auto max-w-3xl space-y-4 p-4 md:p-6">
    <h1 className="text-xl font-semibold">Manage shared project</h1>
    {!(eligible || preserve) || !workspaceId || !project || !session?.user.id
      ? <p role="alert">A known project and current shared access are required.</p>
      : <KnownProjectDetails key={`${session.user.id}:${workspaceId}:${project}`} workspaceId={workspaceId} projectId={project} userId={session.user.id} issueId={deletedScope === issueScope ? undefined : issue ?? undefined} onIssueDeleted={() => setDeletedScope(issueScope)} />}
    {issueScope && deletedScope === issueScope && <p role="status">Known issue deleted.</p>}
  </section>
}

export default function KnownProjectManagementPage() {
  return <Suspense fallback={<p role="status">Checking project access…</p>}><KnownProjectManagement /></Suspense>
}
