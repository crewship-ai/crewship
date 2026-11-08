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
 /** Where the form renders once the server admits agents (the Artifacts Agent drawer); inline by default. */
 place?:(form:React.ReactNode)=>React.ReactNode
}
interface Attempt {life:number;chatId:string;content:string;sent:boolean;messageId?:string}
const object=(value:unknown):value is Record<string,unknown>=>Boolean(value)&&typeof value==='object'&&!Array.isArray(value)
const id=(value:unknown):value is string=>typeof value==='string'&&value.length>0&&value.length<=256&&!value.includes('\0')
const JSON_HEADERS={'Content-Type':'application/json'}
/** Records the message without a chat turn, so a private instruction reaches no agent log. */
const INSTRUCTION_ONLY={native_craft_instruction:true}
/** No run follows this send, so only its delivery can stall. */
const DELIVERY_LIMIT_MS=30_000
const MAX_INSTRUCTION=4000
const NOT_CREATED='No task was created.'

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
 if(!flag)return null
 const form=<AgentStartForm {...props}/>
 return props.place?<>{props.place(form)}</>:form
}

/** One deliberate start: a new chat, the person's persisted message without a
 * chat turn, then the unchanged task contract bound to that message. Nothing is
 * ever replayed. */
function AgentStartForm({workspaceId,pageId,humanId,enabled,holder=null,onStarted,place}:NativeCraftAgentStartProps){
 // Placed in a drawer (the Artifacts Agent tab), the form is the content itself: no toggle to open it.
 const placed=Boolean(place)
 const [open,setOpen]=React.useState(placed),[agentId,setAgentId]=React.useState(''),[text,setText]=React.useState(''),[consent,setConsent]=React.useState(false)
 const [preparing,setPreparing]=React.useState(false),[attempt,setAttempt]=React.useState<Attempt>(),[error,setError]=React.useState<string>()
 const directory=useWorkspaceAgentDirectory(open?workspaceId:undefined)
 // Synchronous guards: a second press and a re-render must not start or post twice.
 const claimed=React.useRef(false),posted=React.useRef(false),call=React.useRef<AbortController|undefined>(undefined)
 // Ending an attempt invalidates it at once, also for effects of the same commit.
 const lifetime=React.useRef(0)
 const finish=React.useCallback((message?:string)=>{lifetime.current++;claimed.current=false;posted.current=false;setPreparing(false);setAttempt(undefined);setConsent(false);setError(message)},[])
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
  onSendRefused(){if(claimed.current)finish(`The agent is not accepting instructions right now. ${NOT_CREATED}`)},
 })
 const {connectionStatus,isStreaming,markHistoryUnavailable,sendMessage}=chat
 React.useEffect(()=>()=>call.current?.abort(),[])
 // The host retired this start. A message may already be in the chat, but no task follows it.
 React.useEffect(()=>{
  if(enabled||!claimed.current||posted.current)return
  call.current?.abort();finish(`Starting the agent was interrupted. ${NOT_CREATED}`)
 },[enabled,finish])
 React.useEffect(()=>{
  if(!enabled||!attempt||attempt.life!==lifetime.current||attempt.sent||connectionStatus!=='connected')return
  // This control renders no transcript; the sequenced echo must still be applied.
  markHistoryUnavailable()
  if(sendMessage(attempt.content,INSTRUCTION_ONLY)===false){finish(`The instruction could not be sent. ${NOT_CREATED}`);return}
  setAttempt(current=>current&&{...current,sent:true})
 },[enabled,attempt,connectionStatus,markHistoryUnavailable,sendMessage,finish])
 const chatId=attempt?.chatId
 React.useEffect(()=>{
  if(!chatId)return
  const timer=setTimeout(()=>{if(!posted.current)finish(`The instruction could not be confirmed. ${NOT_CREATED}`)},DELIVERY_LIMIT_MS)
  return()=>clearTimeout(timer)
 },[chatId,finish])
 // The server's done settles the send; the task binds what its echo named.
 React.useEffect(()=>{
  if(!enabled||!attempt?.sent||attempt.life!==lifetime.current||isStreaming||posted.current)return
  if(!attempt.messageId){finish(`The instruction was not saved in the chat. ${NOT_CREATED}`);return}
  posted.current=true
  const abort=new AbortController();call.current=abort
  void(async()=>{
   let message='Starting the task was not confirmed.'
   try{
    const response=await apiFetch(`/api/v1/pages/${encodeURIComponent('@'+pageId)}/craft/tasks?${new URLSearchParams({workspace_id:workspaceId})}`,{method:'POST',cache:'no-store',redirect:'error',skipRefresh:true,signal:abort.signal,headers:JSON_HEADERS,body:JSON.stringify({chat_id:attempt.chatId,message_id:attempt.messageId,whole_document:true})})
    if(response.status===403)message=`This agent may not edit the document. ${NOT_CREATED}`
    else if(response.status===503)message=`Agents are not available right now. ${NOT_CREATED}`
    const body:unknown=response.status===202?await response.json():null
    if(object(body)&&id(body.work_id)){
     if(abort.signal.aborted)return
     setText('');setOpen(placed);finish();onStarted(body.work_id);return
    }
   }catch{/* Reported below; a lost reply is never sent again. */}
   if(!abort.signal.aborted)finish(message)
  })()
 },[enabled,attempt,isStreaming,pageId,workspaceId,onStarted,finish])
 const instruction=text.trim()
 const agentHolds=holder==='agent'
 const pending=preparing||Boolean(attempt)
 const ready=consent&&Boolean(agentId)&&instruction.length>0&&!pending&&!agentHolds
 async function start(){
  if(!ready||claimed.current)return
  claimed.current=true
  const life=++lifetime.current
  const abort=new AbortController();call.current=abort
  setError(undefined);setPreparing(true)
  try{
   const created=await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/chats?${query}`,{method:'POST',cache:'no-store',redirect:'error',skipRefresh:true,signal:abort.signal,headers:JSON_HEADERS,body:JSON.stringify({origin:'UI'})})
   const body:unknown=created.ok?await created.json():null
   if(!object(body)||!id(body.id))throw Error('native_agent_chat_unconfirmed')
   const profile=await apiFetch(`/api/v1/chats/${encodeURIComponent(body.id)}/execution-profile?${query}`,{signal:abort.signal,cache:'no-store'})
   const mode:unknown=profile.ok?await profile.json():null
   if(!object(mode)||mode.mode!=='trusted')throw Error('native_agent_chat_not_trusted')
   if(abort.signal.aborted||life!==lifetime.current)return
   setPreparing(false);setAttempt({life,chatId:body.id,content:instruction,sent:false})
  }catch{if(!abort.signal.aborted)finish(`The chat with the agent could not be prepared. ${NOT_CREATED}`)}
 }
 if(!enabled)return null
 return <div role="group" aria-label="Edit with an agent" className={placed?"space-y-2 text-sm":"space-y-2 border-b px-3 py-2 text-sm"}>
  {!open&&!placed&&<Button size="sm" variant="outline" onClick={()=>setOpen(true)}>Edit with an agent</Button>}
  {open&&<>
   <label className="flex items-center gap-2">Agent
    <select aria-label="Agent" className="rounded-md border bg-background px-2 py-1" value={agentId} disabled={pending} onChange={event=>setAgentId(event.target.value)}>
     <option value="">Choose an agent</option>
     {directory.agents?.map(agent=><option key={agent.id} value={agent.id}>{agent.name}</option>)}
    </select>
   </label>
   {directory.error&&!directory.agents&&<p role="alert">The agent list could not be loaded.</p>}
   <Textarea aria-label="Instruction for the agent" placeholder="What should the agent do in the document?" maxLength={MAX_INSTRUCTION} value={text} disabled={pending} onChange={event=>setText(event.target.value)}/>
   {holder==='human'&&<p className="text-muted-foreground">Someone is editing the document now. The agent will not change it; it prepares a proposal for review.</p>}
   {agentHolds&&<p className="text-muted-foreground">An agent is editing the document. You can start another task when it finishes.</p>}
   <label className="flex items-center gap-2"><input type="checkbox" checked={consent} disabled={pending} onChange={event=>setConsent(event.target.checked)}/>The agent may edit the whole document</label>
   <p className="text-muted-foreground">The instruction is saved in a new chat with the agent and editing starts right after.</p>
   <div className="flex gap-2">
    <Button size="sm" disabled={!ready} onClick={()=>void start()}>Start agent</Button>
    {!placed&&<Button size="sm" variant="outline" disabled={pending} onClick={()=>setOpen(false)}>Cancel</Button>}
   </div>
   {pending&&<p role="status">Saving the instruction…</p>}
  </>}
  {error&&<p role="alert">{error}</p>}
 </div>
}
