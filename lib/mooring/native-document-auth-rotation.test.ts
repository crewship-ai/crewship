// @vitest-environment node
import {afterEach,describe,expect,it,vi} from "vitest"
import {createNativeDocumentClient} from "./native-document-client"
import type {NativeSaveRequest} from "./single-editor-save"
const signal=()=>new AbortController().signal
const admission=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))
const scope={workspaceId:"ws",pageId:"page"}
function deferred<T>(){let resolve!:(value:T)=>void;const promise=new Promise<T>(done=>resolve=done);return{promise,resolve}}
afterEach(()=>vi.useRealTimers())
describe('native credential rotation',()=>{
 it('proactively obtains a fresh cookie before admitting a nearly expired connection',async()=>{
  const pending=deferred<'ok'>(),refresh=vi.fn(()=>pending.promise),send=vi.fn().mockImplementation(admission)
  const client=createNativeDocumentClient({...scope,fetch:send,refresh,bearer:()=>false})
  const opening=client.open(signal());void opening.catch(()=>{})
  try{expect(refresh).toHaveBeenCalledOnce();expect(send).not.toHaveBeenCalled();pending.resolve('ok');await opening;expect(send).toHaveBeenCalledOnce();expect(refresh.mock.invocationCallOrder[0]).toBeLessThan(send.mock.invocationCallOrder[0])}finally{pending.resolve('ok');await opening.catch(()=>{});client.close()}
 })
 it.each(['auth_failed','retryable_failed'] as const)('does not create a private admission after initial refresh %s',async outcome=>{
  const refresh=vi.fn().mockResolvedValue(outcome),send=vi.fn().mockImplementation(admission)
  const client=createNativeDocumentClient({...scope,fetch:send,refresh,bearer:()=>false})
  try{await expect(client.open(signal())).rejects.toThrow('native_auth_refresh_unconfirmed');expect(send).not.toHaveBeenCalled()}finally{client.close()}
 })
 it('cannot admit after the mounted owner closes while initial refresh is pending',async()=>{
  const pending=deferred<'ok'>(),refresh=vi.fn(()=>pending.promise),send=vi.fn().mockImplementation(admission)
  const client=createNativeDocumentClient({...scope,fetch:send,refresh,bearer:()=>false}),opening=client.open(signal());void opening.catch(()=>{})
  client.close();pending.resolve('ok');await expect(opening).rejects.toThrow('native_client_unavailable');expect(send).not.toHaveBeenCalled()
 })
 it('keeps fixed bearer authentication outside cookie rotation',async()=>{
  const refresh=vi.fn().mockResolvedValue('ok'),send=vi.fn().mockImplementation(admission)
  const client=createNativeDocumentClient({...scope,fetch:send,refresh,bearer:()=>true});try{await client.open(signal());expect(refresh).not.toHaveBeenCalled();expect(send).toHaveBeenCalledOnce()}finally{client.close()}
 })
 it('replaces only the authenticated metadata stream after fresh reauthorization, preserving one in-flight save',async()=>{
  vi.useFakeTimers({toFake:['Date']});const refresh=vi.fn().mockResolvedValue('ok'),saving=deferred<Response>(),lifetime=new AbortController(),messages:unknown[]=[]
  const streams:Array<ReadableStreamDefaultController<Uint8Array>>=[];let duplicate=true
  const send=vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
   const path=String(input)
   if(path.includes('/sessions?'))return admission()
   if(path.includes('/sessions/reauthorize?'))return new Response('{"renewed":true}')
   if(path.includes('/document/save?'))return saving.promise
   if(path.includes('/events?')){
    if(streams.length===1&&duplicate){duplicate=false;return new Response('{}',{status:409})}
    const body=new ReadableStream<Uint8Array>({start(controller){streams.push(controller);init?.signal?.addEventListener('abort',()=>controller.error(init.signal?.reason),{once:true})}})
    return new Response(body,{headers:{'Content-Type':'text/event-stream'}})
   }
   throw Error('unexpected native route')
  })
  const client=createNativeDocumentClient({...scope,fetch:send,refresh,bearer:()=>false});await client.open(lifetime.signal)
  const running=client.events(lifetime.signal,(event,value)=>messages.push([event,value]));void running.catch(()=>{})
  const request:NativeSaveRequest={workspaceId:'ws',pageId:'page',applicationId:'wordcraft',enginePin:'e'.repeat(64),baseVersion:'9007199254740993',generation:'9007199254740995',sessionId:'public-session',editorRevision:'opaque-last-keyboard-edit',contentHash:'sha256:'+'a'.repeat(64),bytes:new Uint8Array([1,2,3])}
  const savingRequest=client.save(request,lifetime.signal);void savingRequest.catch(()=>{})
  try{
   await vi.waitFor(()=>expect(streams).toHaveLength(1));streams[0].enqueue(new TextEncoder().encode('event: lease\ndata: {"mine":true,"can_write":true,"generation":"9007199254740995"}\n\n'))
   await vi.waitFor(()=>expect(messages).toHaveLength(1));vi.setSystemTime(Date.now()+5*60*1000)
   await expect(client.reauthorize(lifetime.signal)).resolves.toBe(true)
   await vi.waitFor(()=>expect(streams).toHaveLength(2),{timeout:2000});expect(refresh).toHaveBeenCalledTimes(2)
   expect(send.mock.calls.filter(([url])=>String(url).includes('/sessions?'))).toHaveLength(1)
   expect(send.mock.calls.filter(([url])=>String(url).includes('/document/save?'))).toHaveLength(1)
   expect(send.mock.calls.filter(([url])=>String(url).includes('/lease/acquire?'))).toHaveLength(0)
   expect(send.mock.calls.filter(([url])=>String(url).includes('/events?'))).toHaveLength(3)
   for(const [,init]of send.mock.calls)if(new Headers(init?.headers).has('X-Craft-Session'))expect(new Headers(init?.headers).get('X-Craft-Session')).toBe('b'.repeat(64))
   streams[1].enqueue(new TextEncoder().encode('event: lease\ndata: {"mine":true,"can_write":true,"generation":"9007199254740995"}\n\n'))
   await vi.waitFor(()=>expect(messages).toHaveLength(2));saving.resolve(new Response('{"version":"9007199254740994"}'));await expect(savingRequest).resolves.toEqual({version:'9007199254740994'})
   streams[1].enqueue(new TextEncoder().encode('event: session_retired\ndata: {"delivery":"stopped","draft":"purge","reason":"read_revoked"}\n\n'));await running
   expect(messages.at(-1)).toEqual(['session_retired',{delivery:'stopped',draft:'purge',reason:'read_revoked'}])
  }finally{lifetime.abort();client.close();saving.resolve(new Response('{}'));await Promise.allSettled([running,savingRequest])}
 })
})

it('coalesces overlapping periodic reauthorization without duplicating a fresh credential binding',async()=>{
 const renewal=deferred<Response>(),send=vi.fn().mockImplementationOnce(admission).mockImplementation(()=>renewal.promise)
 const refresh=vi.fn().mockResolvedValue('ok'),client=createNativeDocumentClient({...scope,fetch:send,refresh,bearer:()=>false})
 await client.open(signal())
 const first=client.reauthorize(signal()),second=client.reauthorize(signal());void first.catch(()=>{});void second.catch(()=>{})
 try{await vi.waitFor(()=>expect(send).toHaveBeenCalledTimes(2));renewal.resolve(new Response('{"renewed":true}'));await expect(Promise.all([first,second])).resolves.toEqual([true,true]);expect(send.mock.calls.filter(([url])=>String(url).includes('/sessions/reauthorize?'))).toHaveLength(1)}finally{renewal.resolve(new Response('{"renewed":false}'));await Promise.allSettled([first,second]);client.close()}
})

it('never retries an initial metadata conflict as if it were a planned credential rotation',async()=>{
 const send=vi.fn().mockImplementationOnce(admission).mockResolvedValue(new Response('{}',{status:409})),client=createNativeDocumentClient({...scope,fetch:send})
 try{await client.open(signal());await expect(client.events(signal(),()=>{})).rejects.toThrow('native_stream_unavailable');expect(send).toHaveBeenCalledTimes(2)}finally{client.close()}
})
it('bounds duplicate metadata GET retries after a planned rotation and never reacquires custody',async()=>{
 let events=0;const lifetime=new AbortController()
 const send=vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const path=String(input);if(path.includes('/sessions?'))return admission();if(path.includes('/reauthorize?'))return new Response('{"renewed":true}')
  if(path.includes('/events?')){events++;if(events>1)return new Response('{}',{status:409});return new Response(new ReadableStream<Uint8Array>({start(controller){init?.signal?.addEventListener('abort',()=>controller.error(init.signal?.reason),{once:true})}}),{headers:{'Content-Type':'text/event-stream'}})}
  throw Error('a metadata rotation attempted another route')
 })
 const client=createNativeDocumentClient({...scope,fetch:send});await client.open(lifetime.signal);const running=client.events(lifetime.signal,()=>{});void running.catch(()=>{})
 try{await vi.waitFor(()=>expect(events).toBe(1));await client.reauthorize(lifetime.signal);await expect(running).rejects.toThrow('native_stream_unavailable');expect(events).toBe(10);expect(send.mock.calls.some(([url])=>String(url).includes('/lease/'))).toBe(false)}finally{lifetime.abort();client.close();await running.catch(()=>{})}
})
