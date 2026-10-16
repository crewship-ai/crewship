import { act,renderHook,waitFor } from '@testing-library/react'
import { QueryClient,QueryClientProvider } from '@tanstack/react-query'
import { it,expect,vi } from 'vitest'
import type { ReactNode } from 'react'
import { apiFetch } from '@/lib/api-fetch'
import { useArtifactCopy } from '../use-artifact-copy'
vi.mock('@/lib/api-fetch',()=>({apiFetch:vi.fn()}))
vi.mock('@/hooks/use-pages',()=>({pagesKeys:{all:(workspace:string)=>['pages',workspace]}}))
const input={source_revision:7,slug:'my-report',name:'My report'}
function setup(){const client=new QueryClient({defaultOptions:{mutations:{retry:false},queries:{retry:false}}});const wrapper=({children}:{children:ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>;return {client,...renderHook(()=>useArtifactCopy('ws','source'),{wrapper})}}
it('preserves exact mutation ID after lost response even when input property order changes',async()=>{
 const requests:Record<string,unknown>[]=[]
 vi.mocked(apiFetch).mockImplementation(async(_url,init)=>{requests.push(JSON.parse(String(init?.body)));if(requests.length===1)throw new Error('lost');return new Response(JSON.stringify({page_slug:'my-report',revision:1}),{status:200})})
 const hook=setup()
 await act(async()=>{await expect(hook.result.current.mutateAsync(input)).rejects.toThrow('could not be confirmed')})
 await act(async()=>{await hook.result.current.mutateAsync({name:input.name,slug:input.slug,source_revision:input.source_revision})})
 expect(requests[0]).toEqual(requests[1]);expect(requests[0].mutation_id).toEqual(expect.any(String))
 hook.unmount();hook.client.clear()
})
it('access cancellation during cache invalidation cannot resolve a late success',async()=>{
 vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({page_slug:'my-report',revision:1}),{status:201}))
 const hook=setup();let release!:()=>void
 const invalidate=vi.spyOn(hook.client,'invalidateQueries').mockImplementation(()=>new Promise<void>(resolve=>{release=resolve}))
 let promise!:Promise<unknown>
 act(()=>{promise=hook.result.current.mutateAsync(input).catch(error=>error)})
 await waitFor(()=>expect(invalidate).toHaveBeenCalled())
 expect(invalidate).toHaveBeenCalled()
 await act(async()=>{hook.result.current.cancel();release();expect(await promise).toBeInstanceOf(Error)})
 expect(hook.result.current.data).toBeUndefined()
 hook.unmount();hook.client.clear()
})
