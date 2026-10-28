// @vitest-environment node
import { describe,it,expect,vi } from "vitest"
import {createServer,type Server} from "node:http"
import type {AddressInfo} from "node:net"
import {createNativeDocumentClient} from "./native-document-client"
import {NativeSaveError,type NativeSaveRequest} from "./single-editor-save"
const signal=()=>new AbortController().signal
const request:NativeSaveRequest={workspaceId:"ws",pageId:"page",applicationId:"wordcraft",enginePin:"e".repeat(64),baseVersion:"9007199254740993",generation:"9007199254740995",sessionId:"public-session",editorRevision:"10:47:3",contentHash:"sha256:"+"a".repeat(64),bytes:new Uint8Array([0,128,255])}
describe("native document client",()=>{
 it("keeps the admission bearer in closure and preserves exact byte and int64 string snapshots",async()=>{
  const calls:Array<{url:string;init?:RequestInit}>=[]
  const send=vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{calls.push({url:String(input),init});return new Response(JSON.stringify(calls.length===1?{session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}:{version:"9007199254740994"}),{status:200})})
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send})
  await expect(client.open(signal())).resolves.toEqual({sessionId:"public-session",applicationId:"wordcraft"})
  await client.save(request,signal())
  expect(calls[1].url).toBe("/api/v1/pages/%40page/craft/document/save?workspace_id=ws")
  expect(new Headers(calls[1].init?.headers).get("X-Craft-Session")).toBe("b".repeat(64))
  const body=JSON.parse(String(calls[1].init?.body))
  expect(body).toEqual({...request,bytes:"AID/"})
  expect(String(calls[1].init?.body)).not.toContain("b".repeat(64))
  client.close();await expect(client.save(request,signal())).rejects.toThrow()
  expect(calls).toHaveLength(2)
 })
 it("does not retry unknown save outcomes or turn a generic403 into read revocation",async()=>{
  const send=vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))).mockRejectedValueOnce(new Error("connection lost")).mockResolvedValueOnce(new Response('{}',{status:403}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.save(request,signal())).rejects.toThrow("connection lost");expect(send).toHaveBeenCalledTimes(2)
  try {await client.reconcile(request,signal());expect.fail("denied reconciliation")}catch(error){expect(error).toBeInstanceOf(NativeSaveError);expect((error as NativeSaveError).reason).toBe("write_revoked")}
 })
})

describe("native mounted lifecycle",()=>{
 const admission=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))
 it("captures scope before asynchronous admission",async()=>{
  let finish!:(r:Response)=>void
  const options={workspaceId:"ws",pageId:"page",fetch:vi.fn().mockImplementationOnce(()=>new Promise<Response>(resolve=>finish=resolve)).mockResolvedValue(new Response('{}'))}
  const client=createNativeDocumentClient(options);const opened=client.open(signal());options.pageId="other";options.workspaceId="other";await vi.waitFor(()=>expect(finish).toBeTypeOf("function"));finish(admission());await opened
  await expect(client.save(request,signal())).resolves.toEqual({})
 })
 it("does not deliver an in-flight response after closing",async()=>{
  let finish!:(r:unknown)=>void
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce({ok:true,status:200,json:()=>new Promise(resolve=>finish=resolve)})
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  const saving=client.save(request,signal());await vi.waitFor(()=>expect(finish).toBeTypeOf('function'));client.close();finish({version:"2"});await expect(saving).rejects.toThrow("native_client_unavailable")
 })
 it("reads whole native bytes and pins with exact int64 quota",async()=>{
  const snapshot={version:"9007199254740993",bytes:"AID/",applicationId:"wordcraft",enginePin:"e".repeat(64),editorBundle:"a".repeat(64),editorURL:"/editors/wordcraft/b/"+"a".repeat(64)+"/index.html",quota:{usedBytes:"9007199254740993",limitBytes:"9223372036854775807",warning:false,retention:"keep-all"}}
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify(snapshot)))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.read(signal())).resolves.toEqual({...snapshot,bytes:new Uint8Array([0,128,255])})
  expect(String(send.mock.calls[1][0])).toContain("document?workspace_id=ws")
  expect(new Headers(send.mock.calls[1][1].headers).get("X-Craft-Session")).toBe("b".repeat(64))
 })
 it("never promotes a malformed snapshot or mismatched application",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify({version:2,bytes:"AID/",applicationId:"gridcraft"})))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal());await expect(client.read(signal())).rejects.toThrow("invalid_native_document")
 })
})

describe("native authorized metadata stream",()=>{
 const admission=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))
 it("decodes split SSE metadata with the closure bearer and no mutation retry",async()=>{
  const text='event: lease\ndata: {"generation":"9007199254740995","holder":null}\n\nevent: session_retired\ndata: {"delivery":"stopped","reason":"authority_changed","draft":"purge"}\n\n'
  const stream=new ReadableStream<Uint8Array>({start(c){for(const part of [text.slice(0,17),text.slice(17,43),text.slice(43)])c.enqueue(new TextEncoder().encode(part));c.close()}})
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(stream,{headers:{'Content-Type':'text/event-stream'}}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal());const events:unknown[]=[]
  await client.events(signal(),(event,value)=>events.push([event,value]))
  expect(events).toEqual([["lease",{generation:"9007199254740995",holder:null}],["session_retired",{delivery:"stopped",reason:"authority_changed",draft:"purge"}]])
  expect(new Headers(send.mock.calls[1][1].headers).get('X-Craft-Session')).toBe('b'.repeat(64));expect(send).toHaveBeenCalledTimes(2)
 })
 it("rejects oversized metadata rather than buffering arbitrary document content",async()=>{
  const stream=new ReadableStream<Uint8Array>({start(c){c.enqueue(new TextEncoder().encode('data: '+ 'x'.repeat(65536)));c.close()}})
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(stream,{headers:{'Content-Type':'text/event-stream'}}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal());await expect(client.events(signal(),()=>{})).rejects.toThrow('invalid_native_stream')
 })
 it("accepts a valid reauthorization no-op without changing the mounted admission",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response('{"renewed":false}'))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.reauthorize(signal())).resolves.toBe(false)
  expect(send).toHaveBeenCalledTimes(2)
  expect(new Headers(send.mock.calls[1][1]?.headers).get("X-Craft-Session")).toBe("b".repeat(64))
 })
 it.each(["401","403","invalid","network"])("rejects failed reauthorization %s instead of treating it as a valid no-op",async reason=>{
  const send=vi.fn().mockResolvedValueOnce(admission())
  if(reason==="network")send.mockRejectedValueOnce(Error("network unavailable"))
  else send.mockResolvedValueOnce(new Response(reason==="invalid"?'{"renewed":"false"}':'{}',{status:reason==="invalid"?200:Number(reason)}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.reauthorize(signal())).rejects.toThrow()
  expect(send).toHaveBeenCalledTimes(2)
 })
 it("reauthorizes explicitly without creating or replacing the mounted session",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response('{"renewed":true}'))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal());await expect(client.reauthorize(signal())).resolves.toBe(true)
  expect(String(send.mock.calls[1][0])).toContain('sessions/reauthorize?');expect(send).toHaveBeenCalledTimes(2)
 })
})

describe('native lease actions',()=>{
 it('binds every action to the current admission and keeps generations as strings',async()=>{
  const send=vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({session_token:'b'.repeat(64),session_id:'public-session',application_id:'wordcraft',page_id:'page'}))).mockResolvedValue(new Response('{"mine":true,"generation":"9007199254740995"}'))
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send});await client.open(signal())
  await client.leaseAction('handoff',{generation:'9007199254740995',request_id:'public-request'},signal())
  expect(String(send.mock.calls[1][0])).toContain('lease/handoff?')
  expect(JSON.parse(send.mock.calls[1][1].body)).toEqual({generation:'9007199254740995',request_id:'public-request'})
  expect(new Headers(send.mock.calls[1][1].headers).get('X-Craft-Session')).toBe('b'.repeat(64))
  await expect(client.leaseAction('release',{generation:'01'},signal())).rejects.toThrow('invalid_native_lease_action');expect(send).toHaveBeenCalledTimes(2)
 })
})

async function listening(server:Server){await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve));return `http://127.0.0.1:${(server.address() as AddressInfo).port}`}
async function stopped(server:Server){await new Promise<void>((resolve,reject)=>{server.close(error=>error?reject(error):resolve());server.closeAllConnections()})}
it('does not forward a private native admission across a real cross-origin redirect',async()=>{
 vi.unstubAllGlobals() // This test owns both loopback listeners and intentionally uses Node fetch.
 let foreignCalls=0
 const foreign=createServer((_req,res)=>{foreignCalls++;res.setHeader('Content-Type','application/json');res.end('{}')})
 const foreignURL=await listening(foreign)
 const originServer=createServer((req,res)=>{
  if(req.url?.includes('/sessions?')){res.setHeader('Content-Type','application/json');res.end(JSON.stringify({session_token:'b'.repeat(64),session_id:'public-session',application_id:'wordcraft',page_id:'page'}));return}
  res.writeHead(302,{Location:foreignURL+'/capture'});res.end()
 })
 const origin=await listening(originServer)
 const send:typeof fetch=(input,init)=>fetch(new URL(String(input),origin),init)
 const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send})
 try{await client.open(signal());await expect(client.lease(signal())).rejects.toThrow();expect(foreignCalls).toBe(0)}finally{client.close();await stopped(originServer);await stopped(foreign)}
})
it('does not refresh and automatically replay a native mutation through the default API wrapper',async()=>{
 vi.unstubAllGlobals() // Restore Node fetch before installing the scoped loopback transport.
 let mutations=0,refreshes=0
 const server=createServer((req,res)=>{
  res.setHeader('Content-Type','application/json')
  if(req.url?.includes('/sessions?')){res.end(JSON.stringify({session_token:'b'.repeat(64),session_id:'public-session',application_id:'wordcraft',page_id:'page'}));return}
  if(req.url==='/api/auth/token/refresh'){refreshes++;res.end('{}');return}
  mutations++;if(mutations===1){res.writeHead(401);res.end('{"error":"session_expired"}')}else res.end('{"version":"9007199254740994"}')
 })
 const origin=await listening(server),realFetch=globalThis.fetch.bind(globalThis)
 vi.stubGlobal('fetch',((input:RequestInfo|URL,init?:RequestInit)=>realFetch(new URL(String(input),origin),init)) satisfies typeof fetch)
 const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page'})
 try{await client.open(signal());const initialRefreshes=refreshes;expect(initialRefreshes).toBe(1);await expect(client.save(request,signal())).rejects.toBeInstanceOf(NativeSaveError);expect(mutations).toBe(1);expect(refreshes).toBe(initialRefreshes)}finally{client.close();vi.unstubAllGlobals();await stopped(server)}
})
