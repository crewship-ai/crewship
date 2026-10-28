"use client"

import {useEffect,useMemo,useState} from "react"
import {apiFetch,AUTH_EVENT} from "@/lib/api-fetch"
import {validNativeVersion} from "@/lib/mooring/native-draft"

interface Scope {workspaceId:string;pageId:string;humanId:string;enabled?:boolean}
interface Event {operation:string;status:number;holder_kind?:string}
interface Task {state:string;uncertain:boolean;inflight:boolean;events:Event[];version?:string}
const object=(value:unknown):value is Record<string,unknown>=>Boolean(value)&&typeof value==='object'&&!Array.isArray(value)
const id=(value:unknown):value is string=>typeof value==='string'&&value.length>0&&value.length<=256&&!value.includes('\0')
function taskView(raw:unknown,workId:string,scope:Scope):Task {
 if(!object(raw)||raw.work_id!==workId||!id(raw.state)||!id(raw.agent_id)||typeof raw.run_id!=='string'||raw.run_id.length>256||!['wordcraft','gridcraft'].includes(String(raw.application))||typeof raw.uncertain!=='boolean'||typeof raw.operation_inflight!=='boolean'||!Array.isArray(raw.events)||raw.events.length>64)throw Error('invalid_native_task_status')
 const events:Event[]=[]
 for(const event of raw.events){
  if(!object(event)||!['read','acquire','renew','apply','release'].includes(String(event.operation))||!Number.isInteger(event.status)||Number(event.status)<100||Number(event.status)>599||event.holder_kind!==undefined&&!['human','agent','unknown'].includes(String(event.holder_kind)))throw Error('invalid_native_task_event')
  events.push({operation:String(event.operation),status:Number(event.status),holder_kind:event.holder_kind as string|undefined})
 }
 let version:string|undefined
 if(raw.last_committed_receipt!==undefined){
  const receipt=raw.last_committed_receipt
  if(!object(receipt)||receipt.workspaceId!==scope.workspaceId||receipt.pageId!==scope.pageId||receipt.applicationId!==raw.application||receipt.sessionId!=='native-agent:'+raw.run_id||receipt.phase!=='committed'||receipt.issuedBy!=='crewship'||receipt.durability!=='node'||!validNativeVersion(receipt.version)||receipt.version==='0'||typeof receipt.candidateHash!=='string'||!/^sha256:[a-f0-9]{64}$/.test(receipt.candidateHash)||receipt.contentHash!==receipt.candidateHash)throw Error('invalid_native_task_receipt')
  version=receipt.version
 }
 return {state:raw.state,uncertain:raw.uncertain,inflight:raw.operation_inflight,events,version}
}

/** An explicit task link selects a read-only projection under the human's
 * current Page rights. It cannot grant or resume an agent run. */
export function NativeCraftAgentTaskStatus({workspaceId,pageId,humanId,enabled=false}:Scope){
 const workId=typeof window==='undefined'?null:new URLSearchParams(window.location.search).get('craft_task_id')
 // Each scope transition gets a fresh lifetime, including A → B → A.
 // Reusing a string scope key could expose an old receipt before revalidation.
 const key=useMemo(()=>({humanId,workspaceId,pageId,enabled,workId}),[humanId,workspaceId,pageId,enabled,workId])
 const [value,setValue]=useState<{key:object;selected:boolean;task?:Task;error?:boolean}>()
 useEffect(()=>{
  if(!enabled||!humanId)return
  if(!id(workId))return
  let active=true,timer:ReturnType<typeof setTimeout>|undefined,call:AbortController|undefined
  const retire=()=>{active=false;clearTimeout(timer);call?.abort();setValue(undefined)}
  window.addEventListener(AUTH_EVENT,retire)
  const poll=async()=>{
   call=new AbortController();const current=call,timeout=setTimeout(()=>current.abort(),5000)
   let terminal=false
   try{
    const query=new URLSearchParams({workspace_id:workspaceId})
    const response=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/tasks/${encodeURIComponent(workId)}?${query}`,{method:'GET',cache:'no-store',redirect:'error',skipRefresh:true,signal:current.signal})
    if(!response.ok){terminal=[401,403,404,503].includes(response.status);throw Error('native_task_unavailable')}
    terminal=true
    const task=taskView(await response.json(),workId,{workspaceId,pageId,humanId,enabled})
    terminal=false
    if(active&&!current.signal.aborted)setValue({key,selected:true,task})
   }catch{if(active){if(terminal)retire();setValue({key,selected:true,error:true})}}
   finally{clearTimeout(timeout);if(active)timer=setTimeout(()=>{void poll()},2000)}
  }
  void poll()
  return()=>{active=false;clearTimeout(timer);call?.abort();window.removeEventListener(AUTH_EVENT,retire)}
 },[key,enabled,humanId,workspaceId,pageId,workId])
 if(!enabled||value?.key!==key||!value.selected)return null
 if(value.error||!value.task)return <p role="alert" className="border-b px-3 py-2 text-sm">Stav úlohy agenta není dostupný.</p>
 const task=value.task,states:Record<string,string>={queued:'Úloha agenta čeká',starting:'Agent se spouští',running:'Agent pracuje',succeeded:'Úloha agenta dokončena',failed:'Úloha agenta skončila chybou',cancelled:'Úloha agenta zrušena',needs_reconciliation:'Výsledek úlohy se ověřuje'}
 const lastAcquire=task.events.findLast(event=>event.operation==='acquire')
 const humanBusy=lastAcquire?.status===409&&lastAcquire.holder_kind==='human'
 return <aside aria-label="Úloha agenta v dokumentu" className="border-b px-3 py-2 text-sm" aria-live="polite">
  <p>{states[task.state]??'Stav úlohy se ověřuje'}</p>
  {humanBusy&&<p>Agent nepřevzal úpravy: dokument upravoval člověk.</p>}
  {(task.uncertain||task.inflight)&&<p>Výsledek zápisu se ověřuje.</p>}
  {task.version&&<p>Poslední potvrzená verze {task.version}</p>}
 </aside>
}
