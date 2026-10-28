"use client"

import { useRef, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"
import { documentRequest } from "@/hooks/use-page-document"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { usePageCreationAccess } from "@/hooks/use-page-creation-access"
import { createEmptyWordCraftCheckpoint } from "@/lib/mooring/wordcraft-create"
import { openWordCraftSession } from "@/lib/mooring/wordcraft-host-session"
import { commandRequest } from "@/lib/mooring/wordcraft-command-session"
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

type CreateFormat = "native"|"sheet"|"wordcraft"|"doc"|"commands"

const SLUG = /^[a-z0-9]+(-[a-z0-9]+)*$/

export function NewDocumentDialog({workspaceId,onClose,onCreated}:{workspaceId:string;onClose:()=>void;onCreated:(slug:string,nativePageId?:string)=>void}) {
  const [name,setName]=useState("")
  const [slug,setSlug]=useState("")
  const [frozen,setFrozen]=useState(false)
  const [format,setFormat]=useState<CreateFormat>()
  const intent=useRef<{name:string;slug:string;pageId?:string;attempted:boolean;commandAttempted?:boolean;nativeAttempted?:boolean;nativeAdmission?:{token:string;generation:string};format:CreateFormat;sdkPrefix?:string;checkpoint?:string;initialize?:{id:string;schema:3;page_id:string;body:object}}>(undefined)
  const allowed=usePageCreationAccess(workspaceId)
  const availability=useDocumentsAvailability(workspaceId)
  const selectedFormat=format ?? (availability.wordcraftNative ? "native" : availability.gridcraftNative === true ? "sheet" : availability.wordcraftSDKPrefix ? "wordcraft" : availability.data === true ? "doc" : availability.wordcraftCommands ? "commands" : "doc")
  const formatAvailable=selectedFormat === "native" ? availability.wordcraftNative === true : selectedFormat === "sheet" ? availability.gridcraftNative === true : selectedFormat === "commands" ? availability.wordcraftCommands === true : selectedFormat === "wordcraft" ? Boolean(availability.wordcraftSDKPrefix) : availability.data === true
  const client=useQueryClient()
  const create=useMutation({mutationFn:async()=>{
    if (!allowed || !formatAvailable) throw new Error("Document creation is unavailable.")
    const original=intent.current ??= {name:name.trim(),slug,attempted:false,format:selectedFormat,sdkPrefix:availability.wordcraftSDKPrefix}
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
    if (original.format === "native" || original.format === "sheet") {
      const application=original.format === "sheet" ? "gridcraft" : "wordcraft"
      const base=`/api/v1/pages/${encodeURIComponent("@"+pageId)}/craft/`
      const query=new URLSearchParams({workspace_id:workspaceId})
      if (!original.nativeAdmission) {
        if (original.nativeAttempted) throw new Error("Native initialization could not be confirmed. Check the original Page before starting again.")
        const bytes=application === "gridcraft"
          ? (await import("@/lib/mooring/gridcraft-empty")).emptyGridCraftNativeDocument()
          : (await import("@/lib/mooring/wordcraft-native-create")).createEmptyNativeWord()
        let binary="";for(let i=0;i<bytes.length;i+=32768)binary+=String.fromCharCode(...bytes.subarray(i,i+32768))
        original.nativeAttempted=true
        const initialized=await apiFetch(`${base}initialize?${query}`,{method:"POST",redirect:"error",skipRefresh:true,headers:{"Content-Type":"application/json"},body:JSON.stringify({application_id:application,bytes:btoa(binary)})})
        const value=await initialized.json().catch(()=>null)
        if (!initialized.ok || value?.application_id!==application || value?.page_id!==pageId || value?.version!=="1" || typeof value?.session_token!=="string" || !/^[a-f0-9]{64}$/.test(value.session_token) || typeof value?.generation!=="string" || !/^[1-9][0-9]*$/.test(value.generation)) throw new Error("Native initialization could not be confirmed. Check the original Page before starting again.")
        original.nativeAdmission={token:value.session_token,generation:value.generation}
      }
      // Creation does not keep a live editing connection. Release only the
      // exact initialized custody; the mounted editor admits its own session.
      let released=false
      try{released=(await apiFetch(`${base}lease/release?${query}`,{method:"POST",redirect:"error",skipRefresh:true,headers:{"Content-Type":"application/json","X-Craft-Session":original.nativeAdmission.token},body:JSON.stringify({generation:original.nativeAdmission.generation})})).ok}catch{/* The release may have committed before its response was lost. */}
      if (!released) {
        // Initialization is already confirmed. A fresh authorized read may
        // open it safely; the editor starts read-only and never adopts this
        // creation admission or reacquires stale custody.
        const available=await apiFetch(`${base}availability?${query}`,{cache:"no-store",redirect:"error",skipRefresh:true})
        if(!available.ok || (await available.json()).enabled!==true)throw new Error("The created document could not be reopened. Check the original Page before starting again.")
      }
      return {slug:original.slug,nativePageId:pageId}
    }
    if (original.format === "commands") {
      // Only this just-created empty Page is eligible. On an ambiguous create
      // response, a user retry READS/restores the same original Page; it never
      // repeats initialization or converts an existing pilot document.
      const initialize = !original.commandAttempted
      original.commandAttempted = true
      const opened = await commandRequest(workspaceId, pageId, {action:"open",write:true,...(initialize ? {create:true} : {})}, new AbortController().signal)
      if (opened.outcome !== null || opened.projection.revision !== 0) throw new Error("The original empty command document could not be confirmed.")
      return original.slug
    }
    if (original.format === "wordcraft") {
      if (!original.sdkPrefix) throw new Error("WordCraft creation is unavailable.")
      original.checkpoint ??= await createEmptyWordCraftCheckpoint(original.sdkPrefix)
      // A failed or ambiguous response keeps the SAME Page/checkpoint for retry;
      // the host's initialize path never replaces an existing native document.
      await openWordCraftSession(workspaceId,pageId,true,new AbortController().signal,original.checkpoint)
      return original.slug
    }
    const input=original.initialize ??= {id:crypto.randomUUID(),schema:3,page_id:pageId,body:{type:"doc",content:[{type:"paragraph",attrs:{id:crypto.randomUUID()}}]}}
    const acknowledged=await documentRequest(workspaceId,original.slug,"/initialize",input)
    verifyInitialization(acknowledged,input.id)
    // A numeric initialization fence is not an editor state. Opening the Page
    // performs a fresh authorized read with the opaque public epoch.
    return original.slug
  },onSuccess:async saved=>{
    await client.invalidateQueries({queryKey:pagesKeys.all(workspaceId)})
    if(typeof saved==="string")onCreated(saved);else onCreated(saved.slug,saved.nativePageId);onClose()
  }})
  const canSubmit=allowed && formatAvailable && !create.isPending && Boolean(name.trim()) && SLUG.test(slug)
  const submit=()=>{if(canSubmit)create.mutate()}
  // The kit's create surface (dialogs-through-kit guard): Esc, the overlay
  // and the header close all go through onOpenChange, which a pending create
  // refuses so the original intent is not lost mid-request.
  return <CreateSurface open size="sm" onOpenChange={open=>{if(!open && !create.isPending)onClose()}} onSubmit={submit} className="touch-form">
    <CreateSurfaceHeader icon={FilePlus2} context="Artifacts" title="New Doc" description="Create a document owned by you." onClose={create.isPending ? undefined : onClose} />
    <CreateSurfaceBody className="flex flex-col gap-3">
      {(availability.wordcraftNative || availability.gridcraftNative === true || availability.wordcraftSDKPrefix || availability.wordcraftCommands) && <CreateSurfaceField label="Format" htmlFor="document-format"><select id="document-format" className={CREATE_SURFACE_INPUT} disabled={frozen || create.isPending} value={selectedFormat} onChange={event=>setFormat(event.target.value as CreateFormat)}>
        {availability.wordcraftNative && <option value="native">Word</option>}{availability.gridcraftNative === true && <option value="sheet">Sheet</option>}{availability.wordcraftSDKPrefix && <option value="wordcraft">WordCraft</option>}{availability.data === true && <option value="doc">Doc</option>}{availability.wordcraftCommands && <option value="commands">WordCraft (test příkazů)</option>}
      </select></CreateSurfaceField>}
      <CreateSurfaceField label="Name" htmlFor="document-name"><Input id="document-name" className={CREATE_SURFACE_INPUT} autoFocus required disabled={frozen || create.isPending} value={name} onChange={event=>{const next=event.target.value;setName(next);if(!slug || slug===name.toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,""))setSlug(next.toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-|-$/g,""))}} /></CreateSurfaceField>
      <CreateSurfaceField label="Page address" htmlFor="document-slug" hint="Lowercase letters, numbers and hyphens."><Input id="document-slug" className={CREATE_SURFACE_INPUT} required pattern="[a-z0-9]+(-[a-z0-9]+)*" disabled={frozen || create.isPending} value={slug} onChange={event=>setSlug(event.target.value)} /></CreateSurfaceField>
      {/* The real audience of a Page created here (pageReach): its owner and every
          workspace OWNER/ADMIN, plus live grants and folders added later. It is
          not a private-audience Document; "My space" lists only those. */}
      <div className="text-sm"><p className="font-medium">Who can open it</p><p className="text-muted-foreground">You, and workspace owners and admins. Others only after you share it.</p></div>
      {!formatAvailable && <p role="status" className="text-label text-muted-foreground">Document creation is unavailable.</p>}
    </CreateSurfaceBody>
    <CreateSurfaceRefusal message={create.error ? create.error.message : null} />
    <CreateSurfaceFooter onCancel={create.isPending ? undefined : onClose} primaryLabel={create.isPending?"Creating…":intent.current?.pageId?"Retry initialization":"Create Doc"} primaryDisabled={!canSubmit} busy={create.isPending} onPrimary={submit} />
  </CreateSurface>
}
