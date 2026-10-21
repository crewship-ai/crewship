"use client"

import { useRestrictedProjection } from "@/hooks/use-restricted-projection"
import { useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useRouter } from "next/navigation"
import { z } from "zod"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle, AlertDialogTrigger } from "@/components/ui/alert-dialog"

const schema = z.object({ id:z.string(), identifier:z.string(), title:z.string(), project_id:z.string(), status:z.string(), version:z.string().regex(/^[a-f0-9]{64}$/), can_delete:z.boolean() })
type Review = z.infer<typeof schema>
class DeletionReadError extends Error { constructor(readonly status:number) {super("Issue deletion access could not be refreshed.")} }

export function SharedIssueDelete({ path, issueId, projectId, userId, disabled, onDeleted }: {path:string;issueId:string;projectId:string;userId?:string;disabled:boolean;onDeleted?:()=>void}) {
 const projection=useRestrictedProjection<Review>(JSON.stringify([userId,path,projectId]))
 const query=useQuery({queryKey:["restricted-issue-deletion",userId,path,projectId],retry:false,staleTime:0,refetchInterval:15_000,refetchOnMount:"always",
  queryFn:async({signal})=>{
   const read=projection.read(signal)
   const response=await apiFetch(`${path}/deletion`,{signal})
   if(!response.ok){read.reject(response.status);throw new DeletionReadError(response.status)}
   const current=schema.parse(await response.json())
   if(current.id!==issueId || current.project_id!==projectId)throw new Error("The issue project changed. Reload and review its current audience.")
   read.accept(current)
   return current
  },
 })
 const visible=projection.value
 const lost=query.error instanceof DeletionReadError && [401,403,404].includes(query.error.status)
 if(lost)return null
 if(!visible && !query.isError)return <p role="status">Checking deletion access…</p>
 return <section className="space-y-2 border-t pt-3" aria-label="Issue deletion">
  {query.isPending && <p role="status" className="text-sm">Checking deletion access…</p>}
  {query.isError && <div><p role="status" className="text-sm">{query.error.message}</p><Button variant="outline" onClick={()=>void query.refetch()}>Refresh deletion access</Button></div>}
  {visible && !visible.can_delete && <p className="text-sm text-muted-foreground">Only Backlog or Cancelled issues without active work or sub-issues can be deleted.</p>}
  {visible?.can_delete && <DeletionConfirmation current={visible} path={path} disabled={disabled || query.isError} onRefresh={()=>{void query.refetch()}} onDeleted={onDeleted} /> }
 </section>
}


// Hard access loss or ineligibility unmounts this state, so restored authority
// requires opening and reviewing a new confirmation.
function DeletionConfirmation({current,path,disabled,onRefresh,onDeleted}:{current:Review;path:string;disabled:boolean;onRefresh:()=>void;onDeleted?:()=>void}) {
 const client=useQueryClient()
 const router=useRouter()
 const [open,setOpen]=useState(false)
 const [review,setReview]=useState<Review|null>(null)
 const unavailable=disabled || !current.can_delete
 const stale=review?.version!==current.version
 const remove=useMutation({retry:false,mutationFn:async()=>{
  if(!review)throw new Error("Review the issue before deleting it.")
  const response=await apiFetch(path,{method:"DELETE",headers:{"Content-Type":"application/json"},body:JSON.stringify({project_id:review.project_id,version:review.version,confirm:true})})
  if(!response.ok)throw new Error(response.status===409?"The issue changed or has active work. Close this confirmation, refresh and review it again.":"Deletion could not be confirmed. Refresh the issue before retrying.")
 },onSuccess:async()=>{setOpen(false);await client.invalidateQueries({queryKey:["restricted-issues"]});if(onDeleted)onDeleted();else router.push("/issues")},onError:onRefresh})
 return ( <AlertDialog open={open} onOpenChange={next=>{if(next)setReview(current);setOpen(next)}}>
   <AlertDialogTrigger asChild><Button variant="destructive" disabled={unavailable || remove.isPending}>Delete issue</Button></AlertDialogTrigger>
   <AlertDialogContent><AlertDialogHeader><AlertDialogTitle>Delete {review?.identifier}?</AlertDialogTitle><AlertDialogDescription>Permanently removes this issue, its comments and attachments. This cannot be undone.</AlertDialogDescription></AlertDialogHeader>
    <p className="text-sm">{review?.title}</p>
    {stale && <p role="status" className="text-sm">This issue changed after you opened the confirmation. Close it and review the current issue.</p>}
    {remove.isError && <p role="alert" className="text-sm">{remove.error.message}</p>}
    <AlertDialogFooter><AlertDialogCancel>Keep issue</AlertDialogCancel><AlertDialogAction variant="destructive" disabled={unavailable || stale || remove.isPending} onClick={event=>{event.preventDefault();if(!unavailable && !stale && !remove.isPending)remove.mutate()}}>Permanently delete issue</AlertDialogAction></AlertDialogFooter>
   </AlertDialogContent>
 </AlertDialog>
 )
}
