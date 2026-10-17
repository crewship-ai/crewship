"use client"

import { useRef, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"
import { documentRequest } from "@/hooks/use-page-document"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { usePageCreationAccess } from "@/hooks/use-page-creation-access"
import { verifyInitialization } from "@/lib/documents/contracts"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"

export function NewDocumentDialog({workspaceId,onClose,onCreated}:{workspaceId:string;onClose:()=>void;onCreated:(slug:string)=>void}) {
  const [name,setName]=useState("")
  const [slug,setSlug]=useState("")
  const [frozen,setFrozen]=useState(false)
  const intent=useRef<{name:string;slug:string;pageId?:string;attempted:boolean;initialize?:{id:string;schema:3;page_id:string;body:object}}>(undefined)
  const allowed=usePageCreationAccess(workspaceId)
  const availability=useDocumentsAvailability(workspaceId)
  const client=useQueryClient()
  const create=useMutation({mutationFn:async()=>{
    if (!allowed || availability.data !== true) throw new Error("Document creation is unavailable.")
    const original=intent.current ??= {name:name.trim(),slug,attempted:false}
    setFrozen(true)
    if (!original.pageId) {
      // Page creation has no idempotency key. An ambiguous response must not
      // create a second Page or adopt an existing Page by its address.
      if (original.attempted) throw new Error("Page creation could not be confirmed. Check the Pages list before starting again.")
      original.attempted=true
      const response=await apiFetch(`/api/v1/pages?workspace_id=${encodeURIComponent(workspaceId)}`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({name:original.name,slug:original.slug,panels:[],allow_empty:true})})
      const value=await response.json().catch(()=>null)
      if (!response.ok) {
        if (response.status>=400 && response.status<500 && ![408,429].includes(response.status)) {intent.current=undefined;setFrozen(false)}
        throw new Error(value?.error ?? "Page creation could not be confirmed.")
      }
      if (typeof value?.id !== "string" || !value.id) throw new Error("Page creation did not confirm its stable identity. Check the Pages list before starting again.")
      original.pageId=value.id
    }
    const pageId=original.pageId
    if (!pageId) throw new Error("The original Page identity is unavailable.")
    const input=original.initialize ??= {id:crypto.randomUUID(),schema:3,page_id:pageId,body:{type:"doc",content:[{type:"paragraph",attrs:{id:crypto.randomUUID()}}]}}
    const acknowledged=await documentRequest(workspaceId,original.slug,"/initialize",input)
    verifyInitialization(acknowledged,input.id)
    // A numeric initialization fence is not an editor state. Opening the Page
    // performs a fresh authorized read with the opaque public epoch.
    return original.slug
  },onSuccess:async saved=>{
    await client.invalidateQueries({queryKey:pagesKeys.all(workspaceId)})
    onCreated(saved);onClose()
  }})
  return <Dialog open onOpenChange={open=>{if(!open && !create.isPending)onClose()}}><DialogContent>
    <DialogHeader><DialogTitle>New Doc</DialogTitle><DialogDescription>Create a private document owned by you.</DialogDescription></DialogHeader>
    <form className="space-y-4" onSubmit={event=>{event.preventDefault();create.mutate()}}>
      <div className="space-y-2"><label htmlFor="document-name">Name</label><Input id="document-name" autoFocus required disabled={frozen || create.isPending} value={name} onChange={event=>{const next=event.target.value;setName(next);if(!slug || slug===name.toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,""))setSlug(next.toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,""))}} /></div>
      <div className="space-y-2"><label htmlFor="document-slug">Page address</label><Input id="document-slug" required pattern="[a-z0-9]+(-[a-z0-9]+)*" disabled={frozen || create.isPending} value={slug} onChange={event=>setSlug(event.target.value)} /><p className="text-xs text-muted-foreground">Lowercase letters, numbers and hyphens.</p></div>
      {create.error && <p role="alert">{create.error.message}</p>}
      {availability.data!==true && <p role="status">Document creation is unavailable.</p>}
      <DialogFooter><Button type="button" variant="ghost" disabled={create.isPending} onClick={onClose}>Cancel</Button><Button type="submit" disabled={!allowed || availability.data!==true || create.isPending || !name.trim() || !slug}>{create.isPending?"Creating…":intent.current?.pageId?"Retry initialization":"Create Doc"}</Button></DialogFooter>
    </form>
  </DialogContent></Dialog>
}
