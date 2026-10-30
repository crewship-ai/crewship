// @vitest-environment node
import {afterEach,expect,it,vi} from "vitest"
import {createNativeDocumentClient} from "./native-document-client"
const scope={workspaceId:"ws",pageId:"page"}
const admission=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))
const packet=(event:string,value:unknown)=>new TextEncoder().encode(`event: ${event}\ndata: ${JSON.stringify(value)}\n\n`)
const lease={generation:"3",mine:false,can_write:false,holder:null}
const retirement={delivery:"stopped",reason:"test_finished",draft:"retain"}
afterEach(()=>vi.useRealTimers())
it.each(["eof","reset"] as const)("reauthorizes the same admission and reconnects after transport %s without retrying mutations",async failure=>{
 let first!:ReadableStreamDefaultController<Uint8Array>,gets=0
 const send=vi.fn(async(input:RequestInfo|URL)=>{
  const url=String(input)
  if(url.includes("/sessions?"))return admission()
  if(url.includes("/sessions/reauthorize?"))return new Response('{"renewed":false}')
  if(!url.includes("/events?"))throw new Error("unexpected mutation or lease route")
  gets++
  return new Response(new ReadableStream<Uint8Array>({start(controller){
   if(gets===1){first=controller;controller.enqueue(packet("lease",lease))}
   else {controller.enqueue(packet("lease",{...lease,generation:"4"}));controller.enqueue(packet("session_retired",retirement));controller.close()}
  }}),{headers:{"Content-Type":"text/event-stream"}})
 })
 const client=createNativeDocumentClient({...scope,fetch:send}),lifetime=new AbortController(),messages:unknown[]=[]
 await client.open(lifetime.signal)
 const running=client.events(lifetime.signal,(event,value)=>messages.push([event,value]));void running.catch(()=>{})
 try{
  await vi.waitFor(()=>expect(messages).toHaveLength(1))
  if(failure==="eof")first.close();else first.error(new TypeError("connection reset"))
  await vi.waitFor(()=>expect(gets).toBe(2))
  await running
  expect(messages).toEqual([["lease",lease],["lease",{...lease,generation:"4"}],["session_retired",retirement]])
  expect(send.mock.calls.map(([url])=>String(url).split("?")[0])).toEqual(["/api/v1/pages/%40page/craft/sessions","/api/v1/pages/%40page/craft/events","/api/v1/pages/%40page/craft/sessions/reauthorize","/api/v1/pages/%40page/craft/events"])
 }finally{lifetime.abort();client.close();await running.catch(()=>{})}
})
it("never reopens the stream after current admission authorization is refused",async()=>{
 let stream!:ReadableStreamDefaultController<Uint8Array>
 const send=vi.fn(async(input:RequestInfo|URL)=>{
  const url=String(input)
  if(url.includes("/sessions?"))return admission()
  if(url.includes("/sessions/reauthorize?"))return new Response('{}',{status:403})
  return new Response(new ReadableStream<Uint8Array>({start(controller){stream=controller;controller.enqueue(packet("lease",lease))}}),{headers:{"Content-Type":"text/event-stream"}})
 })
 const client=createNativeDocumentClient({...scope,fetch:send}),lifetime=new AbortController(),messages:unknown[]=[]
 await client.open(lifetime.signal)
 const running=client.events(lifetime.signal,(_,value)=>messages.push(value));void running.catch(()=>{})
 try{
  await vi.waitFor(()=>expect(messages).toHaveLength(1));stream.error(new TypeError("connection reset"))
  await expect(running).rejects.toThrow("native_session_unavailable")
  expect(send.mock.calls.filter(([url])=>String(url).includes("/events?"))).toHaveLength(1)
  expect(messages).toEqual([lease])
 }finally{lifetime.abort();client.close();await running.catch(()=>{})}
})

it("close cancels an in-flight recovery authorization and never opens another stream",async()=>{
 let first!:ReadableStreamDefaultController<Uint8Array>,renewalSignal:AbortSignal|undefined
 const send=vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const url=String(input)
  if(url.includes("/sessions?"))return admission()
  if(url.includes("/sessions/reauthorize?"))return new Promise<Response>((_,reject)=>{
   renewalSignal=init!.signal!;renewalSignal.addEventListener("abort",()=>reject(renewalSignal!.reason),{once:true})
  })
  return new Response(new ReadableStream<Uint8Array>({start(controller){first=controller;controller.enqueue(packet("lease",lease))}}),{headers:{"Content-Type":"text/event-stream"}})
 })
 const client=createNativeDocumentClient({...scope,fetch:send}),lifetime=new AbortController(),messages:unknown[]=[]
 await client.open(lifetime.signal)
 const running=client.events(lifetime.signal,(_,value)=>messages.push(value));void running.catch(()=>{})
 try{
  await vi.waitFor(()=>expect(messages).toHaveLength(1));first.error(new TypeError("connection reset"))
  await vi.waitFor(()=>expect(renewalSignal).toBeDefined())
  client.close();expect(renewalSignal!.aborted).toBe(true)
  await expect(running).rejects.toThrow()
  expect(send.mock.calls.filter(([url])=>String(url).includes("/events?"))).toHaveLength(1)
 }finally{lifetime.abort();client.close();await running.catch(()=>{})}
})
it("bounds repeated short-lived streams even if each starts with a valid lease",async()=>{
 vi.useFakeTimers()
 let gets=0
 const send=vi.fn(async(input:RequestInfo|URL)=>{
  const url=String(input)
  if(url.includes("/sessions?"))return admission()
  if(url.includes("/sessions/reauthorize?"))return new Response('{"renewed":false}')
  gets++
  return new Response(new ReadableStream<Uint8Array>({start(controller){controller.enqueue(packet("lease",lease));controller.close()}}),{headers:{"Content-Type":"text/event-stream"}})
 })
 const client=createNativeDocumentClient({...scope,fetch:send}),lifetime=new AbortController()
 await client.open(lifetime.signal)
 const running=client.events(lifetime.signal,()=>{});void running.catch(()=>{})
 try{
  await vi.advanceTimersByTimeAsync(17_000)
  expect(gets).toBe(9)
  await expect(running).rejects.toThrow("native_stream_unavailable")
 }finally{lifetime.abort();client.close();await running.catch(()=>{})}
})

it("close cancels recovery coalesced onto an existing periodic authorization",async()=>{
 let first!:ReadableStreamDefaultController<Uint8Array>,renewalSignal:AbortSignal|undefined
 const send=vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const url=String(input)
  if(url.includes("/sessions?"))return admission()
  if(url.includes("/sessions/reauthorize?"))return new Promise<Response>((_,reject)=>{
   renewalSignal=init!.signal!;renewalSignal.addEventListener("abort",()=>reject(renewalSignal!.reason),{once:true})
  })
  return new Response(new ReadableStream<Uint8Array>({start(controller){first=controller;controller.enqueue(packet("lease",lease))}}),{headers:{"Content-Type":"text/event-stream"}})
 })
 const client=createNativeDocumentClient({...scope,fetch:send}),lifetime=new AbortController(),periodic=new AbortController(),messages:unknown[]=[]
 const authorize=vi.spyOn(client,"reauthorize")
 await client.open(lifetime.signal)
 const running=client.events(lifetime.signal,(_,value)=>messages.push(value));void running.catch(()=>{})
 let renewing:Promise<boolean>|undefined
 try{
  await vi.waitFor(()=>expect(messages).toHaveLength(1))
  renewing=client.reauthorize(periodic.signal);void renewing.catch(()=>{})
  await vi.waitFor(()=>expect(renewalSignal).toBeDefined())
  first.error(new TypeError("connection reset"))
  await vi.waitFor(()=>expect(authorize).toHaveBeenCalledTimes(2))
  client.close();expect(renewalSignal!.aborted).toBe(true)
  expect(periodic.signal.aborted).toBe(false)
  await expect(running).rejects.toThrow()
  await expect(renewing).rejects.toThrow()
  expect(send.mock.calls.filter(([url])=>String(url).includes("/events?"))).toHaveLength(1)
 }finally{periodic.abort();lifetime.abort();client.close();await Promise.allSettled([running,renewing]);authorize.mockRestore()}
})

it.each([false,true])("backs off transport-recovery 409 without retrying mutations (permanent hold: %s)",async permanent=>{
 vi.useFakeTimers()
 let first!:ReadableStreamDefaultController<Uint8Array>,busyUntil=0,gets=0,outcome="pending"
 const send=vi.fn(async(input:RequestInfo|URL)=>{
  const url=String(input)
  if(url.includes("/sessions?"))return admission()
  if(url.includes("/sessions/reauthorize?"))return new Response('{"renewed":false}')
  if(!url.includes("/events?"))throw new Error("unexpected mutation or lease route")
  gets++
  if(gets>1&&Date.now()<busyUntil)return new Response('{}',{status:409})
  return new Response(new ReadableStream<Uint8Array>({start(controller){
   if(gets===1){first=controller;controller.enqueue(packet("lease",lease))}
   else{controller.enqueue(packet("lease",{...lease,generation:"4"}));controller.enqueue(packet("session_retired",retirement));controller.close()}
  }}),{headers:{"Content-Type":"text/event-stream"}})
 })
 const client=createNativeDocumentClient({...scope,fetch:send}),lifetime=new AbortController(),messages:unknown[]=[]
 await client.open(lifetime.signal)
 const running=client.events(lifetime.signal,(_,value)=>messages.push(value))
 void running.then(()=>{outcome="resolved"},()=>{outcome="rejected"})
 try{
  await vi.advanceTimersByTimeAsync(0)
  expect(messages).toEqual([lease])
  busyUntil=permanent?Infinity:Date.now()+2500
  first.error(new TypeError("wifi reset"))
  await vi.advanceTimersByTimeAsync(permanent?17000:4000)
  if(permanent){
   expect(outcome).toBe("rejected")
   await expect(running).rejects.toThrow("native_stream_unavailable")
   expect(messages).toEqual([lease]);expect(gets).toBe(9)
  }else{
   expect(outcome).toBe("resolved")
   await running
   expect(messages).toEqual([lease,{...lease,generation:"4"},retirement]);expect(gets).toBe(6)
  }
  expect(send.mock.calls.filter(([url])=>String(url).includes("/sessions?"))).toHaveLength(1)
  expect(send.mock.calls.every(([url])=>/\/(events|sessions|sessions\/reauthorize)\?/.test(String(url)))).toBe(true)
 }finally{lifetime.abort();client.close();await running.catch(()=>{})}
})
