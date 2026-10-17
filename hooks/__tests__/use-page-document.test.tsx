import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook as baseRenderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { decodeUpdate, usePageDocument } from "../use-page-document"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
afterEach(() => { vi.restoreAllMocks(); fetchMock.mockReset() })

it.each(["wrong ID", "stale epoch"])("keeps the draft queued when a successful patch returns a %s receipt", async (mismatch) => {
  const server = documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"paragraph"},content:[{type:"text",text:"base"}]}]},3)
  const snapshot = documentSchema.snapshot(server)
  let posts = 0
  fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      posts++
      const body = JSON.parse(init.body as string)
      return Promise.resolve({ok:true,status:200,json:async()=>({id:mismatch === "wrong ID" ? "another-request" : body.id,epoch:mismatch === "stale epoch" ? "old-authority" : body.epoch,rev:5})})
    }
    return Promise.resolve({ok:true,status:200,json:async()=>({page_id:"page",schema:3,rev:4,epoch:"opaque-authority",generation:1,incarnation:"generation",snapshot,snapshotRev:4,updates:[],blocks:[],content:{type:"doc"},outline:[],can_write:true,can_comment:false,can_resolve:false})})
  })
  const {result,unmount}=renderHook(()=>usePageDocument("workspace","slug","page"))
  try {
    await waitFor(()=>expect(result.current.status).toBe("Saved"))
    act(()=> {
      const text=documentSchema.textNode(result.current.doc,"paragraph")
      if (!(text instanceof Y.XmlText)) throw new Error("Expected document text")
      text.insert(4," local draft")
    })
    await waitFor(()=>expect(posts).toBeGreaterThan(0))
    await waitFor(()=>expect(result.current.status).not.toBe("Saving…"))
    expect(result.current.pending).toBe(1)
    expect(result.current.status).not.toBe("Saved")
    expect(content(result.current.doc).toString()).toContain("local draft")
  } finally { unmount();server.destroy() }
})

const renderHook: typeof baseRenderHook = (callback, options) => {
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})
  const wrapper=({children}:{children:ReactNode})=>createElement(QueryClientProvider,{client},children)
  return baseRenderHook(callback,{...options,wrapper})
}

function model(value="base") {return documentSchema.create({type:"doc",content:[{type:"paragraph",attrs:{id:"paragraph"},content:[{type:"text",text:value}]}]},3)}
function content(doc:Y.Doc) {const node=documentSchema.textNode(doc,"paragraph");if(!(node instanceof Y.XmlText))throw new Error("Expected text");return node}
function response(value:unknown,status=200){return Promise.resolve({ok:status<400,status,json:async()=>value})}
function state(doc:Y.Doc,rev=4,epoch="opaque",pageId="page",canWrite=true){return {page_id:pageId,schema:3,rev,epoch,generation:1,incarnation:"incarnation",snapshot:documentSchema.snapshot(doc),snapshotRev:rev,updates:[],blocks:[],content:{type:"doc"},outline:[],can_write:canWrite,can_comment:false,can_resolve:false}}

it("accepts the original exact-retry receipt after the authorized head advances",async()=>{
  const server=model();const bodies:string[]=[];let rev=4
  fetchMock.mockImplementation((_url:string,init?:RequestInit)=>{
    if(init?.method==="POST"){
      bodies.push(init.body as string);const body=JSON.parse(init.body as string)
      if(bodies.length===1){Y.applyUpdate(server,decodeUpdate(body.update));content(server).insert(0,"remote ");rev=10;return response({error:"lost acknowledgment"},503)}
      return response({id:body.id,epoch:body.epoch,rev:5,duplicate:true})
    }
    return response(state(server,rev))
  })
  const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
  try{
    await waitFor(()=>expect(result.current.status).toBe("Saved"));act(()=>content(result.current.doc).insert(4," local"))
    await waitFor(()=>expect(bodies).toHaveLength(2),{timeout:4000})
    await waitFor(()=>expect(result.current.status).toBe("Saved"))
    expect(bodies[1]).toBe(bodies[0]);expect(result.current.pending).toBe(0);expect(result.current.state?.rev).toBe(10)
    expect(content(result.current.doc).toString()).toContain("remote");expect(content(result.current.doc).toString()).toContain("local")
  }finally{unmount();server.destroy()}
})

it("does not recommit an acknowledged update when its authorized refresh fails",async()=>{
 const server=model();let committed=false,readFails=true,posts=0
 fetchMock.mockImplementation((_url:string,init?:RequestInit)=>{
  if(init?.method==="POST"){posts++;const body=JSON.parse(init.body as string);Y.applyUpdate(server,decodeUpdate(body.update));committed=true;return response({id:body.id,epoch:body.epoch,rev:5})}
  return committed && readFails?response({error:"read unavailable"},503):response(state(server,committed?5:4))
 })
 const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
 try{await waitFor(()=>expect(result.current.status).toBe("Saved"));act(()=>content(result.current.doc).insert(0,"local "));await waitFor(()=>expect(result.current.status).toBe("Disconnected"));expect(result.current.pending).toBe(0);readFails=false;await waitFor(()=>expect(result.current.status).toBe("Saved"));expect(posts).toBe(1)}finally{unmount();server.destroy()}
})

it("retains immutable attempted bytes and Page identity while its slug changes",async()=>{
 const server=model();let fail=true;const calls:{url:string;body:string}[]=[]
 fetchMock.mockImplementation((url:string,init?:RequestInit)=>{
  if(init?.method==="POST"){const body=JSON.parse(init.body as string);calls.push({url,body:init.body as string});if(fail)return response({error:"uncertain"},503);Y.applyUpdate(server,decodeUpdate(body.update));return response({id:body.id,epoch:body.epoch,rev:5})}
  return response(state(server,fail?4:5))
 })
 const {result,rerender,unmount}=renderHook(({slug})=>usePageDocument("w",slug,"page"),{initialProps:{slug:"original"}})
 try{await waitFor(()=>expect(result.current.status).toBe("Saved"));const original=result.current.doc;act(()=>content(original).insert(0,"local "));await waitFor(()=>expect(calls.length).toBeGreaterThan(0));rerender({slug:"renamed"});expect(result.current.doc).toBe(original);fail=false;await waitFor(()=>expect(result.current.pending).toBe(0));expect(calls.at(-1)?.url).toContain("/renamed/");expect(calls.at(-1)?.body).toBe(calls[0].body)}finally{unmount();server.destroy()}
})

it.each([400,413,422,"quota"])("retains a rejected local draft and stops retries: %s",async(failure)=>{
 const server=model();let posts=0
 fetchMock.mockImplementation((_url:string,init?:RequestInit)=>init?.method==="POST"?(posts++,response({error:"rejected",code:failure==="quota"?"document_quota":undefined},typeof failure==="number"?failure:429)):response(state(server)))
 const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
 try{await waitFor(()=>expect(result.current.editable).toBe(true));act(()=>content(result.current.doc).insert(0,"recovery "));await waitFor(()=>expect(result.current.recovery).toBe(true));expect(result.current.pending).toBe(1);expect(result.current.editable).toBe(false);expect(content(result.current.doc).toString()).toContain("recovery");await act(async()=>{await new Promise(resolve=>setTimeout(resolve,600))});expect(posts).toBe(1)}finally{unmount();server.destroy()}
})

it("retries rate admission with the exact request instead of discarding the draft",async()=>{
 const server=model();let limited=true;const bodies:string[]=[]
 fetchMock.mockImplementation((_url:string,init?:RequestInit)=>{
  if(init?.method==="POST"){bodies.push(init.body as string);const body=JSON.parse(init.body as string);if(limited)return response({error:"wait",code:"document_rate_limit"},429);return response({id:body.id,epoch:body.epoch,rev:5})}
  return response(state(server,limited?4:5))
 })
 const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
 try{await waitFor(()=>expect(result.current.status).toBe("Saved"));act(()=>content(result.current.doc).insert(0,"local "));await waitFor(()=>expect(result.current.status).toContain("rate limited"));expect(result.current.pending).toBe(1);expect(result.current.recovery).toBe(false);limited=false;await waitFor(()=>expect(result.current.pending).toBe(0));expect(bodies.length).toBeGreaterThan(1);expect(new Set(bodies).size).toBe(1)}finally{unmount();server.destroy()}
})

it.each(["page","epoch"])("rejects a changed %s before replaying local bytes",async(changed)=>{
 const server=model(),foreign=model("foreign private text");let switched=false
 fetchMock.mockImplementation(()=>response(switched?state(foreign,4,changed==="epoch"?"different":"opaque",changed==="page"?"different":"page"):state(server)))
 const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
 try{await waitFor(()=>expect(result.current.status).toBe("Saved"));act(()=>content(result.current.doc).insert(0,"draft "));switched=true;await waitFor(()=>expect(result.current.recovery).toBe(true));expect(content(result.current.doc).toString()).toBe("draft base");expect(fetchMock.mock.calls.some(([,init])=>init?.method==="POST")).toBe(false)}finally{unmount();server.destroy();foreign.destroy()}
})

it("does not hydrate or write an unknown schema",async()=>{
 const server=model();fetchMock.mockImplementation(()=>response({...state(server),schema:999}))
 const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
 try{await waitFor(()=>expect(result.current.status).toContain("newer editor"));expect(result.current.editable).toBe(false);expect(result.current.state).toBeUndefined();expect(fetchMock.mock.calls.some(([,init])=>init?.method==="POST")).toBe(false)}finally{unmount();server.destroy()}
})
it("reads confirmed content without write authority",async()=>{
 const server=model();fetchMock.mockImplementation(()=>response(state(server,4,"opaque","page",false)))
 const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
 try{await waitFor(()=>expect(result.current.status).toBe("Read only"));expect(result.current.editable).toBe(false);expect(content(result.current.doc).toString()).toBe("base");expect(fetchMock.mock.calls.some(([,init])=>init?.method==="POST")).toBe(false)}finally{unmount();server.destroy()}
})
it("stops on malformed saved binary and preserves a recovery state",async()=>{
 const server=model();fetchMock.mockImplementation(()=>response({...state(server),snapshot:"%%%invalid%%%"}))
 const {result,unmount}=renderHook(()=>usePageDocument("w","slug","page"))
 try{await waitFor(()=>expect(result.current.recovery).toBe(true));expect(result.current.error).toContain("Confirmed document content rejected");expect(result.current.editable).toBe(false);await act(async()=>{await new Promise(resolve=>setTimeout(resolve,600))});expect(fetchMock).toHaveBeenCalledTimes(1)}finally{unmount();server.destroy()}
})
