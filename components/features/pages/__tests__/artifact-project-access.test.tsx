// UI lifecycle tests use a cipher seam (as in artifact-project.test.tsx);
// real-browser tests cover draft encryption and server admission.
vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import React from 'react'
import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react'
import {afterEach,expect,it,vi} from 'vitest'
import {ArtifactProject} from '../artifact-project'
const m=vi.hoisted(()=>({human:'owner',fetch:vi.fn()}))
vi.mock('@/hooks/use-auth',()=>({useSessionSafe:()=>({data:{user:{id:m.human}}})}))
vi.mock('@/lib/api-fetch',()=>({tryRefresh:async()=>"ok",apiFetch:(url:string,init:RequestInit)=>url==='/api/auth/session'?Promise.resolve(new Response(JSON.stringify({user:{id:m.human}}))):m.fetch(url,init)}))
vi.mock('@/hooks/use-artifact-agent-dispatch',()=>({useArtifactAgentDispatch:()=>({dispatch:vi.fn(),ready:false})}))
vi.mock('@/components/features/chat/artifact-context/viewer/artifact-slides-preview',()=>({ArtifactSlidesPreview:()=>null}))
vi.mock('@/components/features/chat/artifact-context/viewer/artifact-dashboard-preview',()=>({ArtifactDashboardPreview:()=>null}))
const snapshot={page_id:'p',can_write:true,can_comment:true,can_share:false,can_restore:false,can_send_to_agent:false,latest_version:2,shared_version:1,manifest:{version:2,entrypoint:'index.html',files:[{path:'index.html',content:'<h1>Server content</h1>',media_type:'text/html'},{path:'other.html',content:'<p>Keep this file</p>',media_type:'text/html'}]},versions:[{version:2}],quota:{used_bytes:0,limit_bytes:0,warning:false},comments:[]}
afterEach(()=>{cleanup();m.fetch.mockReset();m.human='owner'})
it('preserves complete local source for copy after a real save403 while retiring server content and controls',async()=>{
 m.fetch.mockImplementation((_url,init)=>Promise.resolve(new Response(JSON.stringify(init.method==='POST'?{error:'write revoked'}:snapshot),{status:init.method==='POST'?403:200})))
 const client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}})
 const view=render(<QueryClientProvider client={client}><ArtifactProject workspaceId="w" slug="demo" /></QueryClientProvider>)
 fireEvent.click(await screen.findByRole('button',{name:'Edit source'}))
 fireEvent.change(screen.getByLabelText('Source file'),{target:{value:'Private human source'}})
 fireEvent.click(screen.getByRole('button',{name:'Save project'}))
 const recovery=await screen.findByLabelText('Source recovery draft')
 expect(recovery).toHaveAttribute('readonly')
 expect((recovery as HTMLTextAreaElement).value).toContain('Private human source')
 expect((recovery as HTMLTextAreaElement).value).toContain('Keep this file')
 expect(screen.queryByTitle('Artifact preview')).toBeNull()
 expect(screen.queryByRole('button',{name:'Save project'})).toBeNull()
 expect(m.fetch.mock.calls.filter(([,init])=>init.method==='POST')).toHaveLength(1)
 m.human='other'
 view.rerender(<QueryClientProvider client={client}><ArtifactProject workspaceId="w" slug="demo" /></QueryClientProvider>)
 await waitFor(()=>expect(screen.queryByLabelText('Source recovery draft')).toBeNull())
})
it('keeps local recovery after a successful polling response downgrades Edit to Read',async()=>{
 let readable=snapshot
 m.fetch.mockImplementation(()=>Promise.resolve(new Response(JSON.stringify(readable))))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 render(<QueryClientProvider client={client}><ArtifactProject workspaceId="w" slug="demo" /></QueryClientProvider>)
 fireEvent.click(await screen.findByRole('button',{name:'Edit source'}))
 fireEvent.change(screen.getByLabelText('Source file'),{target:{value:'Pending private source'}})
 readable={...snapshot,can_write:false,can_comment:false,manifest:{...snapshot.manifest,version:1,files:[{path:'index.html',content:'<h1>Shared only</h1>',media_type:'text/html'}]}}
 await client.invalidateQueries({queryKey:['artifact-project','w','demo']})
 const recovery=await screen.findByLabelText('Source recovery draft')
 expect((recovery as HTMLTextAreaElement).value).toContain('Pending private source')
 expect(recovery).toHaveAttribute('readonly')
 expect(screen.queryByRole('button',{name:'Save project'})).toBeNull()
 expect(screen.queryByLabelText('Source file')).toBeNull()
 expect(screen.getByTitle('Artifact preview').getAttribute('srcdoc')).not.toContain('Server content')
})
