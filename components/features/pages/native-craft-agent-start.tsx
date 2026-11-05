"use client"

import * as React from "react"
import {Button} from "@/components/ui/button"
import {Textarea} from "@/components/ui/textarea"
import {useChat} from "@/hooks/use-chat"
import {useWorkspaceAgentDirectory} from "@/hooks/use-workspace-agent-directory"
import {apiFetch} from "@/lib/api-fetch"
import {resolveWsBase} from "@/lib/server-base"

export interface NativeCraftAgentStartProps {
 workspaceId:string
 pageId:string
 humanId:string
 /** The host's own verdict: write access and no blocked, read-only or retiring state. */
 enabled:boolean
 holder?:'human'|'agent'|null
 onStarted(workId:string):void
}
interface Attempt {chatId:string;content:string;sent:boolean;messageId?:string}
const object=(value:unknown):value is Record<string,unknown>=>Boolean(value)&&typeof value==='object'&&!Array.isArray(value)
const id=(value:unknown):value is string=>typeof value==='string'&&value.length>0&&value.length<=256&&!value.includes('\0')
const JSON_HEADERS={'Content-Type':'application/json'}
/** The ordinary chat turn this message starts only acknowledges; the native task edits. */
const CHAT_TURN_NOTE='Úloha pro nativní dokument (v běžném chatu stačí jednou větou potvrdit, úpravu provede samostatná nativní úloha). '
/** The same bound the acceptance run gives a chat turn. */
const TURN_LIMIT_MS=240_000
const MAX_INSTRUCTION=4000
const NOT_CREATED='Úloha nevznikla.'

/** Availability is Page-scoped and re-read each time the host allows a start. */
export function NativeCraftAgentStart(props:NativeCraftAgentStartProps){
 const {workspaceId,pageId,enabled}=props
 const [flag,setFlag]=React.useState(false)
 React.useEffect(()=>{
  if(!enabled)return
  const abort=new AbortController()
  void(async()=>{
   try{
    const response=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/availability?${new URLSearchParams({workspace_id:workspaceId})}`,{signal:abort.signal,cache:'no-store',redirect:'error'})
    const value:unknown=response.ok?await response.json():null
    if(!abort.signal.aborted)setFlag(object(value)&&value.agents_enabled===true&&value.can_edit===true)
   }catch{if(!abort.signal.aborted)setFlag(false)}
  })()
  return()=>abort.abort()
 },[enabled,workspaceId,pageId])
 return flag?<AgentStartForm {...props}/>:null
}

/** One deliberate start: a new chat, the person's persisted message, then the
 * unchanged task contract bound to that message. Nothing is ever replayed. */
function AgentStartForm({workspaceId,pageId,humanId,enabled,holder=null,onStarted}:NativeCraftAgentStartProps){
 const [open,setOpen]=React.useState(false),[agentId,setAgentId]=React.useState(''),[text,setText]=React.useState(''),[consent,setConsent]=React.useState(false)
 const [preparing,setPreparing]=React.useState(false),[attempt,setAttempt]=React.useState<Attempt>(),[error,setError]=React.useState<string>()
 const directory=useWorkspaceAgentDirectory(open?workspaceId:undefined)
 // Synchronous guards: a second press and a re-render must not start or post twice.
 const claimed=React.useRef(false),posted=React.useRef(false),call=React.useRef<AbortController|undefined>(undefined)
 const finish=React.useCallback((message?:string)=>{claimed.current=false;posted.current=false;setPreparing(false);setAttempt(undefined);setConsent(false);setError(message)},[])
 const query=new URLSearchParams({workspace_id:workspaceId})
 const getToken=React.useCallback(async()=>{
  const response=await apiFetch('/api/v1/ws-token')
  if(!response.ok)return null
  const body:unknown=await response.json()
  return object(body)&&typeof body.token==='string'?body.token:null
 },[])
 const base=resolveWsBase()
 const chat=useChat({workspaceId,sessionId:attempt?.chatId??'',currentUserId:humanId,wsUrl:base?`${base}/ws`:'',getToken,realtimeEnabled:Boolean(attempt),canSend:Boolean(attempt),executionProfile:attempt?'trusted':'pending',
  // The server assigns this id only after it persisted the message.
  onOwnMessageSaved(content,metadata){setAttempt(current=>current?.sent&&!current.messageId&&content===current.content&&id(metadata?.message_id)?{...current,messageId:metadata.message_id}:current)},
  onSendRefused(){finish(`Agent teď nepřijímá zadání. ${NOT_CREATED}`)},
 })
 const {connectionStatus,isStreaming,markHistoryUnavailable,sendMessage}=chat
 React.useEffect(()=>()=>call.current?.abort(),[])
 // The host retired this start. A message may already be in the chat, but no task follows it.
 React.useEffect(()=>{
  if(enabled||!claimed.current||posted.current)return
  call.current?.abort();finish(`Spuštění agenta bylo přerušeno. ${NOT_CREATED}`)
 },[enabled,finish])
 React.useEffect(()=>{
  if(!attempt||attempt.sent||connectionStatus!=='connected')return
  // This control renders no transcript; the sequenced echo must still be applied.
  markHistoryUnavailable()
  if(sendMessage(attempt.content)===false){finish(`Zadání se nepodařilo odeslat. ${NOT_CREATED}`);return}
  setAttempt(current=>current&&{...current,sent:true})
 },[attempt,connectionStatus,markHistoryUnavailable,sendMessage,finish])
 const chatId=attempt?.chatId
 React.useEffect(()=>{
  if(!chatId)return
  const timer=setTimeout(()=>{if(!posted.current)finish(`Agent na zadání neodpověděl. ${NOT_CREATED}`)},TURN_LIMIT_MS)
  return()=>clearTimeout(timer)
 },[chatId,finish])
 // A task never overlaps the ordinary chat turn the same message started.
 React.useEffect(()=>{
  if(!attempt?.sent||isStreaming||posted.current)return
  if(!attempt.messageId){finish(`Zadání se v chatu neuložilo. ${NOT_CREATED}`);return}
  posted.current=true
  const abort=new AbortController();call.current=abort
  void(async()=>{
   let message='Spuštění úlohy nebylo potvrzeno.'
   try{
    const response=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/tasks?${new URLSearchParams({workspace_id:workspaceId})}`,{method:'POST',cache:'no-store',redirect:'error',skipRefresh:true,signal:abort.signal,headers:JSON_HEADERS,body:JSON.stringify({chat_id:attempt.chatId,message_id:attempt.messageId,whole_document:true})})
    if(response.status===403)message=`Tento agent nesmí dokument upravovat. ${NOT_CREATED}`
    else if(response.status===503)message=`Agenti teď nejsou dostupní. ${NOT_CREATED}`
    const body:unknown=response.status===202?await response.json():null
    if(object(body)&&id(body.work_id)){
     if(abort.signal.aborted)return
     setText('');setOpen(false);finish();onStarted(body.work_id);return
    }
   }catch{/* Reported below; a lost reply is never sent again. */}
   if(!abort.signal.aborted)finish(message)
  })()
 },[attempt,isStreaming,pageId,workspaceId,onStarted,finish])
 const instruction=text.trim()
 const agentHolds=holder==='agent'
 const pending=preparing||Boolean(attempt)
 const ready=consent&&Boolean(agentId)&&instruction.length>0&&!pending&&!agentHolds
 async function start(){
  if(!ready||claimed.current)return
  claimed.current=true
  const abort=new AbortController();call.current=abort
  setError(undefined);setPreparing(true)
  try{
   const created=await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/chats?${query}`,{method:'POST',cache:'no-store',redirect:'error',skipRefresh:true,signal:abort.signal,headers:JSON_HEADERS,body:JSON.stringify({origin:'UI'})})
   const body:unknown=created.ok?await created.json():null
   if(!object(body)||!id(body.id))throw Error('native_agent_chat_unconfirmed')
   const profile=await apiFetch(`/api/v1/chats/${encodeURIComponent(body.id)}/execution-profile?${query}`,{signal:abort.signal,cache:'no-store'})
   const mode:unknown=profile.ok?await profile.json():null
   if(!object(mode)||mode.mode!=='trusted')throw Error('native_agent_chat_not_trusted')
   if(abort.signal.aborted)return
   setPreparing(false);setAttempt({chatId:body.id,content:CHAT_TURN_NOTE+instruction,sent:false})
  }catch{if(!abort.signal.aborted)finish(`Chat s agentem se nepodařilo připravit. ${NOT_CREATED}`)}
 }
 if(!enabled)return null
 return <div role="group" aria-label="Úprava agentem" className="space-y-2 border-b px-3 py-2 text-sm">
  {!open&&<Button size="sm" variant="outline" onClick={()=>setOpen(true)}>Upravit agentem</Button>}
  {open&&<>
   <label className="flex items-center gap-2">Agent
    <select aria-label="Agent" className="rounded-md border bg-background px-2 py-1" value={agentId} disabled={pending} onChange={event=>setAgentId(event.target.value)}>
     <option value="">Vyberte agenta</option>
     {directory.agents?.map(agent=><option key={agent.id} value={agent.id}>{agent.name}</option>)}
    </select>
   </label>
   {directory.error&&!directory.agents&&<p role="alert">Seznam agentů se nepodařilo načíst.</p>}
   <Textarea aria-label="Zadání pro agenta" placeholder="Co má agent v dokumentu udělat?" maxLength={MAX_INSTRUCTION} value={text} disabled={pending} onChange={event=>setText(event.target.value)}/>
   {holder==='human'&&<p className="text-muted-foreground">Dokument teď někdo upravuje. Agent ho nezmění, jen připraví návrh ke schválení.</p>}
   {agentHolds&&<p className="text-muted-foreground">Dokument právě upravuje agent. Další úlohu spustíte, až skončí.</p>}
   <label className="flex items-center gap-2"><input type="checkbox" checked={consent} disabled={pending} onChange={event=>setConsent(event.target.checked)}/>Agent smí upravit celý dokument</label>
   <p className="text-muted-foreground">Zadání se odešle do nového chatu s agentem a agent na něj nejdřív odpoví. Úprava začne po jeho odpovědi.</p>
   <div className="flex gap-2">
    <Button size="sm" disabled={!ready} onClick={()=>void start()}>Spustit agenta</Button>
    <Button size="sm" variant="outline" disabled={pending} onClick={()=>setOpen(false)}>Zrušit</Button>
   </div>
   {pending&&<p role="status">Agent přijímá zadání…</p>}
  </>}
  {error&&<p role="alert">{error}</p>}
 </div>
}
