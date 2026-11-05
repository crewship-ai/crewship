// A verified read revocation purges the sealed local draft (draft_read_revoked).
// This pins what the person is told: the draft does not come back.
import React from 'react'
import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {cleanup,fireEvent,render,screen} from '@testing-library/react'
import {afterEach,expect,it,vi} from 'vitest'
import {ArtifactProject} from '../artifact-project'
const m=vi.hoisted(()=>({revoked:false,fetch:vi.fn()}))
vi.mock('@/lib/local-draft-seal',()=>{
 class LocalDraftReadRevoked extends Error {}
 return {LocalDraftReadRevoked,forgetLocalDraftPage(){},isSealedLocalDraft:()=>true,createLocalDraftCipher:async()=>{
  if(m.revoked)throw new LocalDraftReadRevoked('revoked')
  return {seal:async(text:string)=>text,open:async(text:string)=>text,close(){}}
 }}
})
vi.mock('@/hooks/use-auth',()=>({useSessionSafe:()=>({data:{user:{id:'owner'}}})}))
vi.mock('@/lib/api-fetch',()=>({tryRefresh:async()=>'ok',apiFetch:(url:string,init:RequestInit)=>m.fetch(url,init)}))
vi.mock('@/hooks/use-artifact-agent-dispatch',()=>({useArtifactAgentDispatch:()=>({dispatch:vi.fn(),ready:false})}))
vi.mock('@/components/features/chat/artifact-context/viewer/artifact-slides-preview',()=>({ArtifactSlidesPreview:()=>null}))
vi.mock('@/components/features/chat/artifact-context/viewer/artifact-dashboard-preview',()=>({ArtifactDashboardPreview:()=>null}))
const snapshot={page_id:'p',can_write:true,can_comment:true,can_share:false,can_restore:false,can_send_to_agent:false,latest_version:1,shared_version:1,manifest:{version:1,entrypoint:'index.html',files:[{path:'index.html',content:'<h1>Shared</h1>',media_type:'text/html'}]},versions:[{version:1}],quota:{used_bytes:1,limit_bytes:100,warning:false},comments:[]}
afterEach(()=>{cleanup();m.fetch.mockReset();m.revoked=false})
it('says plainly that the local draft was deleted when read access goes',async()=>{
 let allowed=true
 m.fetch.mockImplementation(()=>Promise.resolve(allowed?new Response(JSON.stringify(snapshot)):new Response(JSON.stringify({error:'not found'}),{status:404})))
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 render(<QueryClientProvider client={client}><ArtifactProject workspaceId="w" slug="demo" /></QueryClientProvider>)
 fireEvent.click(await screen.findByRole('button',{name:'Edit source'}))
 fireEvent.change(screen.getByLabelText('Source file'),{target:{value:'Private human source'}})
 allowed=false
 m.revoked=true
 await client.invalidateQueries({queryKey:['artifact-project','w','demo']})
 expect(await screen.findByText(/^Page read access was removed\..*local draft has been deleted/)).toHaveAttribute('role','alert')
 expect(document.body.textContent).not.toContain('Private human source')
})
