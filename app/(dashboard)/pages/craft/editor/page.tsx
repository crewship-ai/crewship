"use client"

import {Suspense,useEffect,useState} from "react"
import {useSearchParams} from "next/navigation"
import {useSession} from "@/hooks/use-auth"
import {usePageWorkspace} from "@/hooks/use-page-workspace"
import {apiFetch} from "@/lib/api-fetch"
import {NativeCraftPreview} from "@/components/features/pages/native-craft-preview"
import {NativeCraftEditorHost} from "@/components/features/pages/native-craft-editor-host"

interface Availability {scope:string;state:"loading"|"enabled"|"preview"|"disabled"|"error"}
export default function CraftPage(){return <Suspense fallback={<p className="p-6">Připojuji…</p>}><NativeWordEntry/></Suspense>}
function NativeWordEntry(){
 const params=useSearchParams(),session=useSession(),workspace=usePageWorkspace()
 const pages=params?.getAll('page_id')??[],workspaces=params?.getAll('workspace_id')??[]
 const humanId=session.data?.user.id,pageId=pages[0],workspaceId=workspace.workspaceId
 const loading=session.status==='loading'||workspace.loading
 const valid=!loading&&session.status==='authenticated'&&Boolean(humanId)&&!workspace.linkUnavailable&&Boolean(workspaceId)&&pages.length===1&&Boolean(pageId)&&pageId.length<=256&&!pageId.includes('\0')&&workspaces.length===1&&workspaces[0]===workspaceId
 const scope=valid?JSON.stringify([humanId,workspaceId,pageId]):null
 const request=valid?`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/availability?${new URLSearchParams({workspace_id:workspaceId!})}`:null
 const [availability,setAvailability]=useState<Availability>()
 useEffect(()=>{
  if(!scope||!request)return
  const abort=new AbortController()
  setAvailability({scope,state:'loading'})
  void apiFetch(request,{signal:abort.signal,cache:'no-store',redirect:'error'}).then(async response=>{
   if(!response.ok)throw Error('native_availability_unconfirmed')
   const value:unknown=await response.json()
   const enabled=Boolean(value)&&typeof value==='object'&&!Array.isArray(value)&&(value as {enabled?:unknown}).enabled===true
   const preview=Boolean(value)&&typeof value==='object'&&!Array.isArray(value)&&(value as {preview_enabled?:unknown}).preview_enabled===true
   if(!abort.signal.aborted)setAvailability({scope,state:enabled?'enabled':preview?'preview':'disabled'})
  }).catch(()=>{if(!abort.signal.aborted)setAvailability({scope,state:'error'})})
  return()=>abort.abort()
 },[scope,request])
 if(loading)return <p className="p-6">Připojuji…</p>
 if(!valid)return <p role="alert" className="p-6">Dokument není dostupný.</p>
 // A previous page or human's result can never authorize this render, even
 // before effect cleanup or when a transport ignores AbortSignal.
 if(availability?.scope!==scope||availability.state==='loading')return <p className="p-6">Připojuji…</p>
 if(availability.state==='error')return <p role="alert" className="p-6">Dostupnost editoru nebyla potvrzena.</p>
 if(availability.state==='preview')return <NativeCraftPreview key={scope!} humanId={humanId!} workspaceId={workspaceId!} pageId={pageId}/>
 if(availability.state!=='enabled')return <p className="p-6">Nativní editor není zapnutý.</p>
 // The host's local UI and journal lifecycle must be remounted for every
 // identity/document scope; recovery data is never carried into a new scope.
 return <NativeCraftEditorHost key={scope!} workspaceId={workspaceId!} pageId={pageId} humanId={humanId!} enabled/>
}
