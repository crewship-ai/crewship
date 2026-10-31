"use client"

import * as React from 'react'
import {useRealtimeEventSafe} from '@/hooks/use-realtime'
import {registerDocumentDraftWriter} from '@/lib/document-queue'
import {readNativePreview,NativePreviewError,type NativePreviewSnapshot} from '@/lib/mooring/native-preview-client'
import {createNativeEditorPort,type NativeEditorPort} from '@/lib/mooring/native-editor-port'
import {downloadNativeExport} from '@/lib/mooring/native-export-download'
import {nativeCraftFrameURL} from './native-craft-editor-host'
import {createEditorFrame,editorFrameSupported} from './workbook-editor-host'

interface Props {humanId:string;workspaceId:string;pageId:string}
/** A different identity/scope removes the old frame synchronously in React's
 * commit; delayed reads cannot insert content into its replacement. */
export function NativeCraftPreview(props:Props){
 const [attempt,setAttempt]=React.useState(0)
 return <PreviewSession key={JSON.stringify([props.humanId,props.workspaceId,props.pageId,attempt])} {...props} retry={()=>setAttempt(n=>n+1)}/>
}
function PreviewSession({humanId,workspaceId,pageId,retry}:Props&{retry:()=>void}){
 const mount=React.useRef<HTMLDivElement>(null)
 const [version,setVersion]=React.useState<string>(),[error,setError]=React.useState<string>()
 const recheck=React.useRef<()=>void>(()=>{})
 // Push: the server names only this person and this page ("re-check access").
 // The answer still comes from the ordinary authorised read; 30 s polling stays.
 useRealtimeEventSafe('page.access_recheck',event=>{if(event.payload.page_id===pageId)recheck.current()})
 useRealtimeEventSafe('realtime.reconnected',()=>recheck.current())
 React.useEffect(()=>{
  const abort=new AbortController()
  let dead=false,reading=false,again=false,port:NativeEditorPort|undefined,frame:HTMLIFrameElement|undefined,current:NativePreviewSnapshot|undefined,deadline:ReturnType<typeof setTimeout>|undefined,readDeadline:ReturnType<typeof setTimeout>|undefined,poll:ReturnType<typeof setInterval>|undefined
  const clearFrame=()=>{clearTimeout(deadline);port?.close();port=undefined;frame?.remove();frame=undefined;current=undefined}
  const fail=(cause?:unknown)=>{
   if(dead)return
   dead=true;abort.abort();clearTimeout(readDeadline);clearInterval(poll);clearFrame();setVersion(undefined)
   setError(cause instanceof NativePreviewError&&cause.status===404?'Sdílený náhled zatím není publikovaný.':'Sdílený náhled není dostupný. Přístup se nepotvrdil.')
  }
  const unregister=registerDocumentDraftWriter(humanId,async()=>fail())
  function show(snapshot:NativePreviewSnapshot){
   if(dead)return
   // Never repurpose the previously qualified frame for a different engine.
   clearFrame()
   const src=nativeCraftFrameURL({...snapshot,quota:{usedBytes:'0',limitBytes:'0',warning:false,retention:'shared-version-only'}},window.location.origin)
   if(!src||!editorFrameSupported())throw Error('isolated_native_preview_unavailable')
   frame=createEditorFrame(document,src,()=>port?.loaded())??undefined
   if(!frame)throw Error('isolated_native_preview_unavailable')
   frame.title=`${snapshot.applicationId==='gridcraft'?'GridCraft':'WordCraft'} · sdílený náhled`
   frame.setAttribute('allow','clipboard-write *')
   mount.current?.append(frame)
   if(!frame.contentWindow)throw Error('isolated_native_preview_unavailable')
   const mountedFrame=frame
   let ready=false
   // This is only an ephemeral port binding, never a server admission/lease.
   // No draft journal, write transition, save API, or custody callback exists.
   const ownPort=createNativeEditorPort({applicationId:snapshot.applicationId,enginePin:snapshot.enginePin,bundle:snapshot.editorBundle,frame:frame.contentWindow,scope:{workspaceId,pageId,baseVersion:snapshot.version,generation:'1',sessionId:`preview:${crypto.randomUUID()}`},requireAccessProtocol:true,
    onChanged(){if(frame===mountedFrame&&ready)fail()},
    onSaveRequest(request){if(frame===mountedFrame&&request.requestId)port?.saveFailed(request.requestId,'Sdílený náhled je pouze pro čtení.')},
    onExport(file){if(!dead&&frame===mountedFrame)try{downloadNativeExport(file)}catch{fail()}},
    onError(){if(frame===mountedFrame)fail()},
    onReady(){void Promise.resolve().then(async()=>{
     if(dead||port!==ownPort)return
     await ownPort.bootstrap(snapshot.bytes,abort.signal)
     if(dead||port!==ownPort)return
     if(await ownPort.setReadOnly(true,abort.signal)!==true)throw Error('readonly_unconfirmed')
     if(dead||port!==ownPort)return
     ready=true;clearTimeout(deadline);setVersion(snapshot.version)
    }).catch(cause=>{if(frame===mountedFrame)fail(cause)})},
   })
   port=ownPort;current=snapshot
   deadline=setTimeout(()=>fail(),20000)
  }
  async function read(){
   if(dead)return
   if(reading){again=true;return}
   reading=true
   readDeadline=setTimeout(()=>fail(),10000)
   try{
    const snapshot=await readNativePreview({workspaceId,pageId},abort.signal)
    if(dead||abort.signal.aborted)return
    if(!current||snapshot.version!==current.version||snapshot.enginePin!==current.enginePin||snapshot.editorBundle!==current.editorBundle||snapshot.editorURL!==current.editorURL||snapshot.applicationId!==current.applicationId){setVersion(undefined);show(snapshot)}
   }catch(cause){fail(cause)}finally{clearTimeout(readDeadline);reading=false;if(again&&!dead){again=false;void read()}}
  }
  const message=(event:MessageEvent)=>port?.handleWindowMessage(event)
  recheck.current=()=>{void read()}
  const focus=()=>{void read()}
  window.addEventListener('message',message);window.addEventListener('focus',focus);window.addEventListener('online',focus)
  poll=setInterval(()=>{void read()},30000)
  void read()
  return()=>{dead=true;recheck.current=()=>{};abort.abort();clearTimeout(readDeadline);clearInterval(poll);unregister();clearFrame();window.removeEventListener('message',message);window.removeEventListener('focus',focus);window.removeEventListener('online',focus)}
 },[humanId,workspaceId,pageId])
 return <section className="flex h-full min-h-96 flex-col"><p className="p-3" role={error?'alert':'status'}>{error??(version?`Sdílený náhled · verze ${version}`:'Načítám sdílený náhled…')}</p>{error&&<button type="button" className="self-start rounded border px-3 py-2 coarse:min-h-[44px]" onClick={retry}>Znovu načíst náhled</button>}<div ref={mount} className="relative min-h-80 flex-1"/></section>
}
