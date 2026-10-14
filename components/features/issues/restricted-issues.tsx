"use client"

import Link from "next/link"
import { useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { requireApiPathSegment } from "@/lib/api-path-segment"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { AccessNotice, useAccessContext } from "@/components/features/restricted/access-notice"
import { Button } from "@/components/ui/button"

const issueSchema=z.object({id:z.string(),identifier:z.string(),title:z.string(),status:z.string(),project_id:z.string(),project_name:z.string(),can_read:z.boolean(),description:z.string().optional()})
type Issue=z.infer<typeof issueSchema>

/** Guests never mount the shared board, activity feed or issue mutation UI. */
export function RestrictedIssues({identifier}:{identifier?:string|null}) {
  const {workspaceId}=useWorkspace()
  const {data:session}=useSession()
  const context=useAccessContext()
  const [offset,setOffset]=useState(0)
  const query=useQuery({
    queryKey:["restricted-issues",session?.user.id,workspaceId,identifier,offset],
    enabled:!!workspaceId && !!session?.user.id, retry:false,refetchInterval:15_000,
    queryFn:async({signal})=>{
      const base=`/api/v1/workspaces/${requireApiPathSegment(workspaceId!)}/restricted-issues`
      const path=identifier?`${base}/${requireApiPathSegment(identifier)}`:`${base}?limit=100&offset=${offset}`
      const response=await apiFetch(path,{signal})
      if(!response.ok)throw new Error(response.status===404?"Access unavailable":"Issues unavailable")
      const data=await response.json()
      return identifier?[issueSchema.parse(data)]:z.array(issueSchema).parse(data)
    },
  })
  return <main className="mx-auto w-full max-w-4xl space-y-4 p-4 md:p-6">
    <div className="flex items-center justify-between"><h1 className="text-lg font-semibold">{identifier?"Issue":"Issues shared with you"}</h1>{identifier?<Link href="/issues" className="text-sm text-primary">Back to issues</Link>:<Button variant="outline" onClick={()=>void query.refetch()}>Refresh</Button>}</div>
    <p className="text-sm text-muted-foreground">Your project grants decide which issues you can list and read.</p>
    {query.isPending && <p role="status">Loading shared issues…</p>}
    {query.isError && <AccessNotice kind="unavailable" title="Issues unavailable" description="Your access may have changed. Retry to check your current project grants." context={context} current="issues" onRetry={()=>void query.refetch()} />}
    {!query.isError && query.data?.length===0 && <AccessNotice kind="no-access" title="No issues shared here" description="There are no issues in the projects shared with you on this page." context={context} current="issues" />}
    {!query.isError && query.data?.map(issue=><IssueCard key={issue.id} issue={issue} detail={!!identifier} />)}
    {!identifier && !query.isError && <div className="flex gap-2">
      <Button variant="outline" disabled={offset===0} onClick={()=>setOffset(Math.max(0,offset-100))}>Previous</Button>
      <Button variant="outline" disabled={(query.data?.length??0)<100} onClick={()=>setOffset(offset+100)}>Next</Button>
    </div>}
  </main>
}

function IssueCard({issue,detail}:{issue:Issue;detail:boolean}) {
  return <article className="space-y-2 rounded-lg border bg-card p-4">
    <p className="text-xs text-muted-foreground">{issue.project_name} · {issue.identifier} · {issue.status}</p>
    <h2 className="font-medium">{!detail && issue.can_read?<Link className="hover:underline" href={`/issues/${encodeURIComponent(issue.identifier)}`}>{issue.title}</Link>:issue.title}</h2>
    {detail && <p className="whitespace-pre-wrap text-sm">{issue.description || "No description."}</p>}
    {!detail && !issue.can_read && <p className="text-xs text-muted-foreground">Ask your administrator for project read access to open details.</p>}
  </article>
}
