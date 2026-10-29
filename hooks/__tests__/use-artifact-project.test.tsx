import React from 'react'
import {flushSync} from 'react-dom'
import {act,renderHook,waitFor,cleanup} from '@testing-library/react'
import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {afterEach,expect,it,vi} from 'vitest'
import {useArtifactProject} from '../use-artifact-project'
const m=vi.hoisted(()=>({human:'A',fetch:vi.fn()}))
vi.mock('@/hooks/use-auth',()=>({useSessionSafe:()=>({data:{user:{id:m.human}}})}))
vi.mock('@/lib/api-fetch',()=>({tryRefresh:async()=>"ok",apiFetch:(url:string,init:RequestInit)=>url==='/api/auth/session'?Promise.resolve(new Response(JSON.stringify({user:{id:m.human}}))):m.fetch(url,init)}))
const snapshot={page_id:'p',can_write:true,latest_version:2,manifest:{version:2,entrypoint:'index.html',files:[]}}
function setup(){const client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});return renderHook(()=>useArtifactProject('w','demo'),{wrapper:({children}:{children:React.ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>})}
afterEach(()=>{cleanup();m.fetch.mockReset();m.human='A'})
it('sends one CAS save with AbortSignal and no credential replay',async()=>{
 m.fetch.mockImplementation((_url,init)=>Promise.resolve(new Response(JSON.stringify(init.method==='POST'?{version:3}:snapshot))))
 const hook=setup();await waitFor(()=>expect(hook.result.current.query.data).toBeTruthy())
 const body={base_version:2,entrypoint:'index.html',files:[{path:'index.html',content:'changed',media_type:'text/html'}]}
 act(()=>hook.result.current.save.mutate(body))
 await waitFor(()=>expect(m.fetch.mock.calls.some(([,init])=>init.method==='POST')).toBe(true))
 const [url,init]=m.fetch.mock.calls.find(([,init])=>init.method==='POST')!
 expect(url).toBe('/api/v1/pages/%40p/artifact/versions?workspace_id=w')
 expect(init).toMatchObject({signal:expect.any(AbortSignal),redirect:'error',skipRefresh:true,body:JSON.stringify(body)})
})
it('aborts delivery and rejects stale mutation callbacks across A to B to A',async()=>{
 m.fetch.mockResolvedValue(new Response(JSON.stringify(snapshot)))
 const hook=setup();await waitFor(()=>expect(hook.result.current.query.data).toBeTruthy())
 const old=hook.result.current.save.mutate
 m.human='B';hook.rerender()
 m.human='A';hook.rerender()
 act(()=>old({base_version:2,entrypoint:'index.html',files:[]}))
 await act(async()=>{})
 expect(m.fetch.mock.calls.filter(([,init])=>init.method==='POST')).toHaveLength(0)
})
it('aborts an inflight save on identity change and ignores its eventual delivery',async()=>{
 let resolve!:(response:Response)=>void
 m.fetch.mockImplementation((_url,init)=>init.method==='POST'?new Promise<Response>(done=>{resolve=done}):Promise.resolve(new Response(JSON.stringify(snapshot))))
 const hook=setup();await waitFor(()=>expect(hook.result.current.query.data).toBeTruthy())
 const success=vi.fn()
 act(()=>hook.result.current.save.mutate({base_version:2,entrypoint:'index.html',files:[]},{onSuccess:success}))
 await waitFor(()=>expect(resolve).toBeTypeOf('function'))
 const signal=m.fetch.mock.calls.find(([,init])=>init.method==='POST')![1].signal as AbortSignal
 m.human='B';hook.rerender();expect(signal.aborted).toBe(true)
 await act(async()=>resolve(new Response('{"version":3}')))
 expect(success).not.toHaveBeenCalled()
})
it('removes cached private content after a denied save without retry',async()=>{
 m.fetch.mockImplementation((_url,init)=>Promise.resolve(new Response(JSON.stringify(init.method==='POST'?{error:'revoked'}:snapshot),{status:init.method==='POST'?403:200})))
 const hook=setup();await waitFor(()=>expect(hook.result.current.query.data).toBeTruthy())
 act(()=>hook.result.current.save.mutate({base_version:2,entrypoint:'index.html',files:[]}))
 await waitFor(()=>expect(hook.result.current.query.isError).toBe(true))
 expect(m.fetch.mock.calls.filter(([,init])=>init.method==='POST')).toHaveLength(1)
})
it.each([false,true])('does not retarget a queued mutation before its mutationFn begins, roundtrip=%s',async roundtrip=>{
 m.fetch.mockImplementation((_url,init)=>Promise.resolve(new Response(JSON.stringify(init.method==='POST'?{version:3}:snapshot))))
 const hook=setup();await waitFor(()=>expect(hook.result.current.query.data).toBeTruthy())
 act(()=>{
  hook.result.current.save.mutate({base_version:2,entrypoint:'index.html',files:[]})
  flushSync(()=>{m.human='B';hook.rerender()})
  if(roundtrip)flushSync(()=>{m.human='A';hook.rerender()})
 })
 await act(async()=>{})
 expect(m.fetch.mock.calls.filter(([,init])=>init.method==='POST')).toHaveLength(0)
})
