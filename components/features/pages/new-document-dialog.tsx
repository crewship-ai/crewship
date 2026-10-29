"use client"

import { useState } from "react"
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
import { useWordDocumentCreation, type DocumentFormat } from "./use-word-document-creation"

const SLUG = /^[a-z0-9]+(-[a-z0-9]+)*$/

export function NewDocumentDialog({workspaceId,onClose,onCreated}:{workspaceId:string;onClose:()=>void;onCreated:(slug:string,nativePageId?:string)=>void}) {
  const [name,setName]=useState("")
  const [slug,setSlug]=useState("")
  const {allowed,availability,selectedFormat,setFormat,formatAvailable,frozen,create,retrying}=useWordDocumentCreation(workspaceId)
  const canSubmit=allowed && formatAvailable && !create.isPending && Boolean(name.trim()) && SLUG.test(slug)
  const submit=()=>{if(canSubmit)create.mutate({name,slug},{onSuccess:saved=>{if(saved.nativePageId)onCreated(saved.slug,saved.nativePageId);else onCreated(saved.slug);onClose()}})}
  // The kit's create surface (dialogs-through-kit guard): Esc, the overlay
  // and the header close all go through onOpenChange, which a pending create
  // refuses so the original intent is not lost mid-request.
  return <CreateSurface open size="sm" onOpenChange={open=>{if(!open && !create.isPending)onClose()}} onSubmit={submit} className="touch-form">
    <CreateSurfaceHeader icon={FilePlus2} context="Artifacts" title="New Doc" description="Create a document owned by you." onClose={create.isPending ? undefined : onClose} />
    <CreateSurfaceBody className="flex flex-col gap-3">
      {(availability.wordcraftNative || availability.gridcraftNative === true || availability.wordcraftSDKPrefix || availability.wordcraftCommands) && <CreateSurfaceField label="Format" htmlFor="document-format"><select id="document-format" className={CREATE_SURFACE_INPUT} disabled={frozen || create.isPending} value={selectedFormat} onChange={event=>setFormat(event.target.value as DocumentFormat)}>
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
    <CreateSurfaceFooter onCancel={create.isPending ? undefined : onClose} primaryLabel={create.isPending?"Creating…":retrying?"Retry initialization":"Create Doc"} primaryDisabled={!canSubmit} busy={create.isPending} onPrimary={submit} />
  </CreateSurface>
}
