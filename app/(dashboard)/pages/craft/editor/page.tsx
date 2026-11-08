"use client"

import {Suspense,useEffect,useState} from "react"
import {useSearchParams} from "next/navigation"
import {useSession} from "@/hooks/use-auth"
import {usePageWorkspace} from "@/hooks/use-page-workspace"
import {apiFetch} from "@/lib/api-fetch"
import {NativeCraftPreview} from "@/components/features/pages/native-craft-preview"
import {NativeCraftEditorHost} from "@/components/features/pages/native-craft-editor-host"
import {verifyNativeReadLoss} from "@/lib/mooring/native-read-loss"
import {useAccessMode} from "@/hooks/use-access-mode"
import {artifactsLibrary} from "@/lib/feature-flags"
import {legacyPagesTarget} from "@/components/features/artifacts-library/legacy-pages-url"
import {LegacyPagesRedirect} from "@/components/features/artifacts-library/legacy-pages-redirect"

interface Availability {scope:string;state:"loading"|"enabled"|"preview"|"disabled"|"error"}
export default function CraftPage(){return <Suspense fallback={<p className="p-6">Connecting…</p>}><CraftEntry/></Suspense>}
// With the library on, a trusted session opens the document in its Artifacts
// detail, /artifacts/<id>, which mounts this same editor host (#5129). Any
// address that does not resolve, and every restricted session, keeps this route.
// The build-time flag never changes at runtime, so the branch is stable.
function CraftEntry(){return artifactsLibrary()?<CraftEntryWithLibrary/>:<NativeWordEntry/>}
function CraftEntryWithLibrary(){
 const params=useSearchParams(),workspace=usePageWorkspace(),accessMode=useAccessMode()
 if(workspace.loading||accessMode==='loading')return <p className="p-6">Connecting…</p>
 const target=workspace.workspaceId&&!workspace.linkUnavailable?legacyPagesTarget('/pages/craft/editor',params?.toString()??'',{workspaceId:workspace.workspaceId,library:true,trusted:accessMode==='trusted'}):null
 return target?.to==='resolve'?<LegacyPagesRedirect target={target}><NativeWordEntry/></LegacyPagesRedirect>:<NativeWordEntry/>
}
function NativeWordEntry(){
 const params=useSearchParams(),session=useSession(),workspace=usePageWorkspace()
 const pages=params?.getAll('page_id')??[],workspaces=params?.getAll('workspace_id')??[]
 const humanId=session.data?.user.id,pageId=pages[0],workspaceId=workspace.workspaceId
 const loading=session.status==='loading'||workspace.loading
 const valid=!loading&&session.status==='authenticated'&&Boolean(humanId)&&!workspace.linkUnavailable&&Boolean(workspaceId)&&pages.length===1&&Boolean(pageId)&&pageId.length<=256&&!pageId.includes('\0')&&workspaces.length===1&&workspaces[0]===workspaceId
 const scope=valid?JSON.stringify([humanId,workspaceId,pageId]):null
 const request=valid?`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/availability?${new URLSearchParams({workspace_id:workspaceId!})}`:null
 const [availability,setAvailability]=useState<Availability>()
 // A workspace gone from this account's directory opens nothing, and may be why.
 // The link still names the immutable scope; only the server's proof erases.
 const linkScope=session.status==='authenticated'&&humanId&&workspace.linkUnavailable&&workspaces.length===1&&workspaces[0]&&pages.length===1&&Boolean(pageId)&&pageId.length<=256&&!pageId.includes('\0')?JSON.stringify([humanId,workspaces[0],pageId]):null
 useEffect(()=>{
  if(!linkScope)return
  const [human,linked,page]=JSON.parse(linkScope) as [string,string,string]
  void verifyNativeReadLoss({human,workspace:linked,page}).catch(()=>{})
 },[linkScope])
 useEffect(()=>{
  if(!scope||!request)return
  const abort=new AbortController()
  // Anything short of the editor may mean this person lost native read, but
  // says neither way: a draft left here is erased only on proof.
  const proveReadLoss=()=>void verifyNativeReadLoss({human:humanId!,workspace:workspaceId!,page:pageId}).catch(()=>{})
  setAvailability({scope,state:'loading'})
  void apiFetch(request,{signal:abort.signal,cache:'no-store',redirect:'error'}).then(async response=>{
   if(!response.ok)throw Error('native_availability_unconfirmed')
   const value:unknown=await response.json()
   const enabled=Boolean(value)&&typeof value==='object'&&!Array.isArray(value)&&(value as {enabled?:unknown}).enabled===true&&(value as {can_edit?:unknown}).can_edit===true
   const preview=Boolean(value)&&typeof value==='object'&&!Array.isArray(value)&&(value as {preview_enabled?:unknown}).preview_enabled===true
   if(abort.signal.aborted)return
   setAvailability({scope,state:enabled?'enabled':preview?'preview':'disabled'})
   if(!enabled)proveReadLoss()
  }).catch(()=>{if(!abort.signal.aborted){setAvailability({scope,state:'error'});proveReadLoss()}})
  return()=>abort.abort()
 },[scope,request,humanId,workspaceId,pageId])
 if(loading)return <p className="p-6">Connecting…</p>
 if(!valid)return <p role="alert" className="p-6">The document is not available.</p>
 // A previous page or human's result can never authorize this render, even
 // before effect cleanup or when a transport ignores AbortSignal.
 if(availability?.scope!==scope||availability.state==='loading')return <p className="p-6">Connecting…</p>
 if(availability.state==='error')return <p role="alert" className="p-6">Editor availability was not confirmed.</p>
 if(availability.state==='preview')return <NativeCraftPreview key={scope!} humanId={humanId!} workspaceId={workspaceId!} pageId={pageId}/>
 if(availability.state!=='enabled')return <p className="p-6">The native editor is not turned on.</p>
 // The host's local UI and journal lifecycle must be remounted for every
 // identity/document scope; recovery data is never carried into a new scope.
 return <NativeCraftEditorHost key={scope!} workspaceId={workspaceId!} pageId={pageId} humanId={humanId!} enabled/>
}
