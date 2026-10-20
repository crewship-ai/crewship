"use client"

import { useRef, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"
import { documentRequest } from "@/hooks/use-page-document"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { usePageCreationAccess } from "@/hooks/use-page-creation-access"
import { verifyInitialization } from "@/lib/documents/contracts"
import { FilePlus2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"

const SLUG = /^[a-z0-9]+(-[a-z0-9]+)*$/

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
  const canSubmit=allowed && availability.data===true && !create.isPending && Boolean(name.trim()) && SLUG.test(slug)
  const submit=()=>{if(canSubmit)create.mutate()}
  // The kit's create surface (dialogs-through-kit guard): Esc, the overlay
  // and the header close all go through onOpenChange, which a pending create
  // refuses so the original intent is not lost mid-request.
  return <CreateSurface open size="sm" onOpenChange={open=>{if(!open && !create.isPending)onClose()}} onSubmit={submit} className="touch-form">
    <CreateSurfaceHeader icon={FilePlus2} context="Artifacts" title="New Doc" description="Create a private document owned by you." onClose={create.isPending ? undefined : onClose} />
    <CreateSurfaceBody className="flex flex-col gap-3">
      <CreateSurfaceField label="Name" htmlFor="document-name"><Input id="document-name" className={CREATE_SURFACE_INPUT} autoFocus required disabled={frozen || create.isPending} value={name} onChange={event=>{const next=event.target.value;setName(next);if(!slug || slug===name.toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,""))setSlug(next.toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,""))}} /></CreateSurfaceField>
      <CreateSurfaceField label="Page address" htmlFor="document-slug" hint="Lowercase letters, numbers and hyphens."><Input id="document-slug" className={CREATE_SURFACE_INPUT} required pattern="[a-z0-9]+(-[a-z0-9]+)*" disabled={frozen || create.isPending} value={slug} onChange={event=>setSlug(event.target.value)} /></CreateSurfaceField>
      {availability.data!==true && <p role="status" className="text-label text-muted-foreground">Document creation is unavailable.</p>}
    </CreateSurfaceBody>
    <CreateSurfaceRefusal message={create.error ? create.error.message : null} />
    <CreateSurfaceFooter onCancel={create.isPending ? undefined : onClose} primaryLabel={create.isPending?"Creating…":intent.current?.pageId?"Retry initialization":"Create Doc"} primaryDisabled={!canSubmit} busy={create.isPending} onPrimary={submit} />
  </CreateSurface>
}
