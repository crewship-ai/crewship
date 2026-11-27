// @vitest-environment node
import { describe,it,expect,vi } from "vitest"
import {createServer,type Server} from "node:http"
import type {AddressInfo} from "node:net"
import {createNativeDocumentClient,initializeNativeWorkbook,NativeExportError,NativeMetadataTransportError} from "./native-document-client"
import {NATIVE_REAUTHORIZE_DEADLINE_MS} from "./native-revocation-bounds"
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
 it("sends a large snapshot gzip-compressed with its declared length, a small one as it is",async()=>{
  const calls:Array<{init?:RequestInit}>=[]
  const send=vi.fn(async(_input:RequestInfo|URL,init?:RequestInit)=>{calls.push({init});return new Response(JSON.stringify(calls.length===1?{session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}:{version:"2"}),{status:calls.length===1?201:200})})
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
  const big={...request,bytes:new Uint8Array(3<<20).map((_,i)=>0x41+(i%7))}
  await client.save(big,signal())
  const h=new Headers(calls[1].init?.headers);expect(h.get("Content-Encoding")).toBe("gzip")
  const packed=calls[1].init?.body as unknown as Uint8Array;expect(packed).toBeInstanceOf(Uint8Array)
  const raw=new Uint8Array(await new Response(new Blob([packed as BlobPart]).stream().pipeThrough(new DecompressionStream("gzip"))).arrayBuffer())
  expect(String(raw.length)).toBe(h.get("X-Craft-Uncompressed-Length"));expect(packed.length).toBeLessThan(raw.length/5)
  expect(JSON.parse(new TextDecoder().decode(raw)).editorRevision).toBe(request.editorRevision)
  await client.save(request,signal())
  expect(new Headers(calls[2].init?.headers).get("Content-Encoding")).toBeNull();expect(typeof calls[2].init?.body).toBe("string")
 })
 it("tells a full version history (quota) from a body the server or a proxy refuses as too large",async()=>{
  const cases:Array<[string,string,string]>=[["application/json",JSON.stringify({detail:"native history quota reached; existing versions retained"}),"quota"],["application/problem+json",JSON.stringify({title:"Request Entity Too Large",detail:"Request body too large"}),"too_large"],["text/html","<html>413 Request Entity Too Large</html>","too_large"]]
  for(const [type,body,expected] of cases){
   let n=0
   const send=vi.fn(async()=>new Response(++n===1?JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}):body,{status:n===1?201:413,headers:{"Content-Type":n===1?"application/json":type}}))
   const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
   try{await client.save(request,signal());expect.fail("413")}catch(error){expect(error).toBeInstanceOf(NativeSaveError);expect((error as NativeSaveError).reason).toBe(expected)}
  }
 })
 it("reports a refused private read apart from an unavailable document",async()=>{
  const opened=()=>new Response(JSON.stringify({session_token:"a".repeat(64),session_id:"session",application_id:"wordcraft",page_id:"page"}),{status:201})
  for(const [status,message] of [[403,"native_private_read_refused"],[503,"native_document_unavailable"]] as const){
   const send=vi.fn(async(url:RequestInfo|URL)=>String(url).includes("sessions")?opened():new Response("{}",{status}))
   const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
   await expect(client.read(signal())).rejects.toThrow(message)
  }
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
 it.each([["critical","critical"],["full","full"],["bogus",undefined]] as const)("passes the quota level %s through only when it is known",async(level,expected)=>{
  const snapshot={version:"2",bytes:"AID/",applicationId:"wordcraft",enginePin:"e".repeat(64),editorBundle:"a".repeat(64),editorURL:"/editors/wordcraft/b/"+"a".repeat(64)+"/index.html",quota:{usedBytes:"96",limitBytes:"100",warning:true,level,retention:"keep-all"}}
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify(snapshot)))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  expect((await client.read(signal())).quota.level).toBe(expected)
 })
 it("never promotes a malformed snapshot or mismatched application",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify({version:2,bytes:"AID/",applicationId:"gridcraft"})))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal());await expect(client.read(signal())).rejects.toThrow("invalid_native_document")
 })
})

describe("native authorized metadata stream",()=>{
 const admission=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))
 it("ends a reauthorization that never answers at its deadline, as a transport failure",async()=>{
  vi.useFakeTimers()
  try{
   const send=vi.fn().mockResolvedValueOnce(admission()).mockImplementationOnce((_:unknown,init:RequestInit)=>new Promise((_,reject)=>init.signal?.addEventListener("abort",()=>reject(init.signal!.reason))))
   const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
   let settled=false
   const attempt=client.reauthorize(signal()).catch((error:unknown)=>{settled=true;return error})
   await vi.advanceTimersByTimeAsync(NATIVE_REAUTHORIZE_DEADLINE_MS-1);expect(settled).toBe(false)
   await vi.advanceTimersByTimeAsync(1)
   expect(await attempt).toBeInstanceOf(NativeMetadataTransportError)
  }finally{vi.useRealTimers()}
 })
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


describe('native shared preview publication',()=>{
 const share={workspaceId:'ws',pageId:'page',applicationId:'wordcraft',enginePin:'e'.repeat(64),sessionId:'public-session',generation:'9007199254740995',baseVersion:'9007199254740993',targetVersion:'9007199254740993'}
 const admission=()=>new Response(JSON.stringify({session_token:'b'.repeat(64),session_id:'public-session',application_id:'wordcraft',page_id:'page'}))
 it('publishes one explicit committed version without putting the bearer in the body',async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify({version:share.targetVersion,durability:'node',issuedBy:'crewship',phase:'shared'})))
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send});await client.open(signal())
  await expect(client.share(share,signal())).resolves.toMatchObject({version:share.targetVersion,phase:'shared'})
  expect(String(send.mock.calls[1][0])).toBe('/api/v1/pages/%40page/craft/document/share?workspace_id=ws')
  expect(send.mock.calls[1][1]).toMatchObject({method:'POST',redirect:'error',cache:'no-store',body:JSON.stringify(share)})
  expect(new Headers(send.mock.calls[1][1].headers).get('X-Craft-Session')).toBe('b'.repeat(64))
  expect(send.mock.calls[1][1].body).not.toContain('b'.repeat(64))
 })
 it.each(['network','403','409','wrong-version'])('does not retry or accept unconfirmed preview publication %s',async(reason)=>{
  const send=vi.fn().mockResolvedValueOnce(admission())
  if(reason==='network')send.mockRejectedValueOnce(Error('lost publication'))
  else send.mockResolvedValueOnce(new Response(reason==='wrong-version'?JSON.stringify({version:'1',durability:'node',issuedBy:'crewship',phase:'shared'}):'{}',{status:reason==='wrong-version'?200:Number(reason)}))
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send});await client.open(signal())
  await expect(client.share(share,signal())).rejects.toThrow();expect(send).toHaveBeenCalledTimes(2)
 })
})

describe("native save attempt retirement (#4899)",()=>{
 it("reconciles a large document without sending it",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response('{}',{status:410}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  const large={...request,bytes:new Uint8Array(20<<20),attemptId:"attempt-0002"}
  await expect(client.reconcile(large,signal())).rejects.toMatchObject({reason:"absent"})
  const init=send.mock.calls[1][1]
  expect(typeof init.body).toBe("string")
  expect(init.headers["Content-Encoding"]).toBeUndefined()
  expect(String(init.body).length).toBeLessThan(4096)
  expect(JSON.parse(String(init.body))).toMatchObject({attemptId:"attempt-0002",contentHash:request.contentHash})
 })
 const admission=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))
 it("maps only a reconcile 410 to absent and sends the attempt id",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response('{}',{status:410})).mockResolvedValueOnce(new Response('{}',{status:410}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  const attempt={...request,attemptId:"attempt-0001"}
  await expect(client.reconcile(attempt,signal())).rejects.toMatchObject({reason:"absent"})
  expect(JSON.parse(String(send.mock.calls[1][1].body)).attemptId).toBe("attempt-0001")
  expect(JSON.parse(String(send.mock.calls[1][1].body))).not.toHaveProperty("bytes")
  // A save never reports absent; 410 there stays an unconfirmed outcome.
  const saving=client.save(attempt,signal());await expect(saving).rejects.toThrow("native_snapshot_unconfirmed");await expect(saving).rejects.not.toBeInstanceOf(NativeSaveError)
 })
})

// #5381: a 503 from the administrator's setting says so; any other 503 stays unconfirmed.
describe('native lease actions while the application is turned off',()=>{
 it.each([
  ['acquire','app_draining','wordcraft','Word'],
  ['request-handoff','app_disabled','gridcraft','Sheet'],
 ] as const)('%s refused with %s reports the application',async(action,reason,application,label)=>{
  const {NativeAppOffError}=await import('./native-app-off')
  const send=vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({session_token:'b'.repeat(64),session_id:'public-session',application_id:application,page_id:'page'}))).mockResolvedValue(new Response(JSON.stringify({error:'off',reason,application}),{status:503}))
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send});await client.open(signal())
  const refused=await client.leaseAction(action,{},signal()).catch((e:unknown)=>e)
  expect(refused).toBeInstanceOf(NativeAppOffError)
  expect((refused as InstanceType<typeof NativeAppOffError>).reason).toBe(reason)
  expect((refused as InstanceType<typeof NativeAppOffError>).label).toBe(label)
 })
 it('keeps an unexplained 503 unconfirmed',async()=>{
  const {NativeAppOffError}=await import('./native-app-off')
  const send=vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({session_token:'b'.repeat(64),session_id:'public-session',application_id:'wordcraft',page_id:'page'}))).mockResolvedValue(new Response('{"error":"busy"}',{status:503}))
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send});await client.open(signal())
  const refused=await client.leaseAction('acquire',{},signal()).catch((e:unknown)=>e)
  expect(refused).not.toBeInstanceOf(NativeAppOffError);expect((refused as Error).message).toBe('native_lease_unconfirmed')
 })
})

describe('native Sheet xlsx export',()=>{
 const opened=()=>new Response(JSON.stringify({session_token:'a'.repeat(64),session_id:'session',application_id:'gridcraft',page_id:'page'}),{status:201})
 const xlsx=(headers:Record<string,string>)=>new Response(new Uint8Array([0x50,0x4b,3,4]),{status:200,headers:{'Content-Type':'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',...headers}})
 async function exporting(answer:()=>Response){
  const calls:{url:string;init?:RequestInit}[]=[]
  const send=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{calls.push({url:String(url),init});return String(url).includes('sessions')?opened():answer()})
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send as unknown as typeof fetch});await client.open(signal())
  return {client,calls}
 }
 it('downloads the confirmed head with the closure admission, named and versioned by the server',async()=>{
  const {client,calls}=await exporting(()=>xlsx({'Content-Disposition':`attachment; filename="Rozpo_et.xlsx"; filename*=UTF-8''Rozpo%C4%8Det%202026.xlsx`,'X-Craft-Version':'7'}))
  const got=await client.exportXlsx(signal())
  expect(got.filename).toBe('Rozpočet 2026.xlsx');expect(got.version).toBe('7');expect(got.blob.size).toBe(4)
  const call=calls.at(-1)!
  expect(call.url).toBe('/api/v1/pages/%40page/craft/document/export.xlsx?workspace_id=ws')
  expect(call.init?.method).toBe('GET');expect(call.init?.cache).toBe('no-store');expect((call.init?.headers as Record<string,string>)['X-Craft-Session']).toBe('a'.repeat(64))
  await client.exportXlsx(signal(),'3');expect(calls.at(-1)!.url).toBe('/api/v1/pages/%40page/craft/document/export.xlsx?workspace_id=ws&version=3')
 })
 it('keeps a file name that cannot leave the download folder, with a fallback',async()=>{
  for(const [disposition,want] of [
   [`attachment; filename="plan.xlsx"`,'plan.xlsx'],
   [`attachment; filename*=UTF-8''..%2F..%2Fetc%2Fpasswd`,'.._.._etc_passwd.xlsx'],
   [`attachment; filename="a\\b"`,'a_b.xlsx'],
   [`attachment; filename*=UTF-8''%E0%A4%A`,'Sheet.xlsx'],
   ['','Sheet.xlsx'],
  ] as const){
   const {client}=await exporting(()=>xlsx({...(disposition?{'Content-Disposition':disposition}:{}),'X-Craft-Version':'1'}))
   expect((await client.exportXlsx(signal())).filename,disposition).toBe(want)
  }
 })
 it('refuses a malformed version before asking and an answer without a version',async()=>{
  const {client,calls}=await exporting(()=>xlsx({'Content-Disposition':'attachment; filename="a.xlsx"'}))
  for(const bad of ['0','01','x','1234567890123456789'])await expect(client.exportXlsx(signal(),bad)).rejects.toMatchObject({reason:'bad_version'})
  expect(calls.filter(c=>c.url.includes('export.xlsx'))).toHaveLength(0)
  await expect(client.exportXlsx(signal())).rejects.toMatchObject({reason:'failed'})
 })
 it('names every refusal for the host, with Retry-After when the server is busy',async()=>{
  for(const [status,reason] of [[400,'bad_version'],[401,'forbidden'],[403,'forbidden'],[404,'not_found'],[413,'too_large'],[422,'not_sheet'],[500,'failed'],[502,'failed'],[503,'busy']] as const){
   const {client}=await exporting(()=>new Response(JSON.stringify({error:'x'}),{status,headers:status===503?{'Retry-After':'30'}:{}}))
   const error=await client.exportXlsx(signal()).catch((e:unknown)=>e)
   expect(error,String(status)).toBeInstanceOf(NativeExportError)
   expect((error as NativeExportError).reason).toBe(reason)
   expect((error as NativeExportError).retryAfterSeconds).toBe(status===503?30:undefined)
  }
 })
})

describe("workbook Sheet transport",()=>{
 const opened=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"gridcraft",page_id:"page"}),{status:201})
 const file=new Uint8Array([0x50,0x4b,3,4,9,8,7,6,5])
 const hash=async(b:Uint8Array)=>"sha256:"+Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",new Uint8Array(b))),x=>x.toString(16).padStart(2,"0")).join("")
 const base64=btoa(String.fromCharCode(...file))
 const meta=(extra:Record<string,unknown>={})=>({version:"3",applicationId:"gridcraft",format:"xlsm",bytes:base64,enginePin:"e".repeat(64),editorBundle:"c".repeat(64),editorURL:"/_editor/b/"+"c".repeat(64)+"/index.html",quota:{usedBytes:"10",limitBytes:"100",warning:false,retention:"keep",level:"none"},...extra})
 const json=(value:unknown)=>Response.json(value)
 it("reads and decodes the stored file with its format",async()=>{
  const send=vi.fn(async(url:RequestInfo|URL)=>String(url).includes("sessions")?opened():json(meta()))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
  expect(await client.read(signal())).toMatchObject({version:"3",bytes:file,format:"xlsm",applicationId:"gridcraft"})
 })
 it("converts an older workbook before reading the file and keeps the original available",async()=>{
  const original=Uint8Array.of(123,125)
  const send=vi.fn<typeof fetch>()
   .mockResolvedValueOnce(opened())
   .mockResolvedValueOnce(Response.json({code:"legacy_workbook"},{status:409}))
   .mockResolvedValueOnce(Response.json({version:"3"}))
   .mockResolvedValueOnce(json(meta({legacySource:true})))
   .mockResolvedValueOnce(new Response(original,{headers:{"Content-Disposition":'attachment; filename="sheet-original.json"'}}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  expect(await client.read(signal())).toMatchObject({version:"3",bytes:file,legacySource:true,format:"xlsm"})
  expect(String(send.mock.calls[2][0])).toContain("/document/migrate-workbook?")
  expect(send.mock.calls[2][1]?.method).toBe("POST")
  expect(await client.originalWorkbook!(signal())).toEqual(original)
 })
 // The engine the server runs, as the installed package names it; a malformed one is not taken.
 it("names the server's engine build when the server gives one, and nothing otherwise",async()=>{
  for(const [engineBuild,want] of [["0.19.1+59f6b6c14a37 patch dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","0.19.1+59f6b6c14a37 patch dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"],[undefined,undefined],["0.19.1 patch x",undefined]] as const){
   const m=meta(engineBuild===undefined?{}:{engineBuild})
   const send=vi.fn(async(url:RequestInfo|URL)=>String(url).includes("sessions")?opened():json(m))
   const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
   expect((await client.read(signal())).engineBuild,String(engineBuild)).toBe(want)
  }
 })
 it("refuses a workbook with an invalid format or invalid file bytes",async()=>{
  for(const raw of [meta({bytes:btoa("not a workbook")}),meta({bytes:"*"}),meta({format:"xls"})]){
   const send=vi.fn(async(url:RequestInfo|URL)=>String(url).includes("sessions")?opened():json(raw))
   const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
   await expect(client.read(signal())).rejects.toThrow("invalid_native_document")
  }
 })
 // The cells the person changed go with the save in X-Craft-Touched, as ASCII JSON (a sheet name
 // outside ASCII is escaped), or "unknown"; a save that does not know sends nothing, which the server takes as unknown.
 it("sends the cells the person changed with the save, and nothing when it does not know",async()=>{
  const seen:Array<string|null>=[]
  const send=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{if(String(url).includes("sessions"))return opened();seen.push(new Headers(init?.headers).get("X-Craft-Touched"));return new Response(JSON.stringify({version:"4"}),{status:200})})
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
  const save:NativeSaveRequest={workspaceId:"ws",pageId:"page",applicationId:"gridcraft",enginePin:"e".repeat(64),baseVersion:"3",generation:"2",sessionId:"public-session",editorRevision:"doc-1:7",contentHash:await hash(file),format:"xlsx",bytes:file}
  await client.save({...save,touched:["'Rozpočet'!B2","'L'!A1:C9"]},signal())
  await client.save({...save,touched:[]},signal())
  await client.save({...save,touched:"unknown"},signal())
  await client.save(save,signal())
  await client.save({...save,touched:Array.from({length:90},(_,i)=>"'"+ "日".repeat(200)+"'!A"+(i+1))},signal())
  expect(seen).toEqual(['["\'Rozpo\\u010det\'!B2","\'L\'!A1:C9"]',"[]","unknown",null,"unknown"])
  expect(JSON.parse(seen[0]!)).toEqual(["'Rozpočet'!B2","'L'!A1:C9"])
 })
 it("saves the file and its binding as JSON, and names refusals by code",async()=>{
  const calls:Array<{url:string;init?:RequestInit}>=[]
  const answers=[new Response(JSON.stringify({version:"4"}),{status:200}),new Response(JSON.stringify({code:"quota",error:"x"}),{status:413}),new Response(JSON.stringify({code:"unpacked_too_large",error:"x"}),{status:413}),new Response(JSON.stringify({code:"encrypted",error:"x"}),{status:415}),new Response(JSON.stringify({code:"conflict",error:"x"}),{status:409})]
  const send=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{if(String(url).includes("sessions"))return opened();calls.push({url:String(url),init});return answers.shift()!})
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
  const save:NativeSaveRequest={workspaceId:"ws",pageId:"page",applicationId:"gridcraft",enginePin:"e".repeat(64),baseVersion:"3",generation:"2",sessionId:"public-session",editorRevision:"doc-1:7",contentHash:await hash(file),attemptId:"att-1",format:"xlsx",bytes:file}
  await expect(client.save(save,signal())).resolves.toEqual({version:"4"})
  const h=new Headers(calls[0].init?.headers)
  expect(calls[0].url).toBe("/api/v1/pages/%40page/craft/document/save?workspace_id=ws")
  const {format,...binding}=save
  expect(format).toBe("xlsx")
  expect(JSON.parse(calls[0].init?.body as string)).toEqual({...binding,bytes:base64})
  expect(h.get("Content-Type")).toBe("application/json")
  expect(h.get("X-Craft-Session")).toBe("b".repeat(64))
  for(const reason of ["quota","too_large","unsupported","conflict"]){
   try{await client.save(save,signal());expect.fail(reason)}catch(error){expect(error).toBeInstanceOf(NativeSaveError);expect((error as NativeSaveError).reason).toBe(reason)}
  }
 })
 // A save that would lose macros, signatures or customXml is refused (422 fidelity_loss) and nothing is stored.
 it("tells a save refused for losing parts of the workbook from other refusals",async()=>{
  const answers=[new Response(JSON.stringify({code:"fidelity_loss",parts:[{code:"vba",count:1}]}),{status:422}),new Response(JSON.stringify({code:"other"}),{status:422})]
  const send=vi.fn(async(url:RequestInfo|URL)=>{if(String(url).includes("sessions"))return opened();return answers.shift()!})
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
  const save:NativeSaveRequest={workspaceId:"ws",pageId:"page",applicationId:"gridcraft",enginePin:"e".repeat(64),baseVersion:"3",generation:"2",sessionId:"public-session",editorRevision:"doc-1:7",contentHash:await hash(file),attemptId:"att-1",format:"xlsm",bytes:file}
  await expect(client.save(save,signal())).rejects.toMatchObject({reason:"fidelity_loss"})
  await expect(client.save(save,signal()),"another 422 is not called a loss of parts").rejects.not.toMatchObject({reason:"fidelity_loss"})
 })
 // A save refused because the Sheet moved on is told apart; Keep mine says so in a header.
 it("tells a save refused because the Sheet moved on from other conflicts, and marks a Keep mine save",async()=>{
  const calls:Array<{url:string;init?:RequestInit}>=[]
  const answers=[new Response(JSON.stringify({code:"moved",head:"5"}),{status:409}),new Response(JSON.stringify({code:"lease"}),{status:409}),new Response(JSON.stringify({version:"6"}),{status:200})]
  const send=vi.fn(async(url:RequestInfo|URL,init?:RequestInit)=>{if(String(url).includes("sessions"))return opened();calls.push({url:String(url),init});return answers.shift()!})
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
  const save:NativeSaveRequest={workspaceId:"ws",pageId:"page",applicationId:"gridcraft",enginePin:"e".repeat(64),baseVersion:"3",generation:"2",sessionId:"public-session",editorRevision:"doc-1:7",contentHash:await hash(file),attemptId:"att-1",format:"xlsx",bytes:file}
  for(const reason of ["moved","conflict"]){
   try{await client.save(save,signal());expect.fail(reason)}catch(error){expect((error as NativeSaveError).reason).toBe(reason)}
  }
  expect(new Headers(calls[0].init?.headers).get("X-Craft-Rebase")).toBeNull()
  await client.save({...save,baseVersion:"5",rebase:"keep-local"},signal())
  expect(new Headers(calls[2].init?.headers).get("X-Craft-Rebase")).toBe("keep-local")
 })
 it("reads a saved version's file and starts a Sheet from an uploaded workbook",async()=>{
  const m=meta({version:"1",currentVersion:"3",current:false,createdAt:"2026-11-26T10:00:00Z",author:{actorId:"user:u",kind:"human",displayName:"Owner"}})
  const send=vi.fn(async(url:RequestInfo|URL)=>String(url).includes("sessions")?opened():json(m))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send as unknown as typeof fetch});await client.open(signal())
  const v=await client.version("1",signal())
  expect(v).toMatchObject({version:"1",currentVersion:"3",current:false,format:"xlsm"});expect(Array.from(v.bytes)).toEqual(Array.from(file))
  const init=vi.fn(async(_url:RequestInfo|URL,_init?:RequestInit)=>new Response("{}",{status:201}))
  await initializeNativeWorkbook({workspaceId:"ws",pageId:"page",file:new Blob([file]),fetch:init as unknown as typeof fetch})
  const [url,sent]=init.mock.calls[0]
  expect(String(url)).toBe("/api/v1/pages/%40page/craft/initialize?workspace_id=ws")
  const h=new Headers(sent?.headers)
  expect(h.get("Content-Type")).toBe("application/json")
  expect(JSON.parse(sent?.body as string)).toEqual({application_id:"gridcraft",bytes:base64})
  await expect(initializeNativeWorkbook({workspaceId:"ws",pageId:"page",file:new Blob([new Uint8Array(32*1024*1024+1)]),fetch:init as unknown as typeof fetch})).rejects.toThrow("native_workbook_too_large")
  expect(init).toHaveBeenCalledTimes(1)
  expect(h.has("X-Craft-Chat")).toBe(false)
  await initializeNativeWorkbook({workspaceId:"ws",pageId:"page",file:new Blob([file]),chatId:"chat_1",fetch:init as unknown as typeof fetch})
  expect(new Headers(init.mock.calls[1][1]?.headers).get("X-Craft-Chat")).toBe("chat_1")
  await expect(initializeNativeWorkbook({workspaceId:"ws",pageId:"page",file:new Blob([file]),chatId:"bad id",fetch:init as unknown as typeof fetch})).rejects.toThrow("invalid_native_workbook")
  expect(init).toHaveBeenCalledTimes(2)
 })
})

// The undo endpoint needs the editor's session and its lease generation.
describe('undo of an agent change',()=>{
 const admission=()=>new Response(JSON.stringify({session_token:'b'.repeat(64),session_id:'s',application_id:'gridcraft',page_id:'page'}))
 it('sends the operation under the session and the lease generation, and hands back the server answer',async()=>{
  const answer=new Response(JSON.stringify({version:'12',skipped:0}),{status:201})
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(answer)
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send});await client.open(signal())
  await expect(client.undo('run-1:req-2','7',signal())).resolves.toBe(answer)
  expect(String(send.mock.calls[1][0])).toBe('/api/v1/pages/%40page/craft/document/undo?workspace_id=ws')
  const init=send.mock.calls[1][1] as RequestInit
  expect(init).toMatchObject({method:'POST',cache:'no-store',body:JSON.stringify({operation_id:'run-1:req-2'})})
  expect(new Headers(init.headers).get('X-Craft-Session')).toBe('b'.repeat(64))
  expect(new Headers(init.headers).get('X-Craft-Generation')).toBe('7')
 })
 it('sends nothing for a malformed operation or generation',async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission())
  const client=createNativeDocumentClient({workspaceId:'ws',pageId:'page',fetch:send});await client.open(signal())
  for(const [op,gen] of [['bad id','7'],['op','0x'],['','7']])await expect(client.undo(op,gen,signal())).rejects.toThrow('native_undo_invalid')
  expect(send).toHaveBeenCalledTimes(1)
 })
})
