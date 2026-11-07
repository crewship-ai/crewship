// A verified read revocation purges the sealed local draft (draft_read_revoked).
// This pins what the person is told: the draft does not come back.
import React from 'react'
import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {act,cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react'
import {afterEach,describe,expect,it,vi} from 'vitest'
import {ArtifactProject} from '../artifact-project'
const m=vi.hoisted(()=>({revoked:false,fetch:vi.fn(),purge:vi.fn(),cipher:undefined as undefined|(()=>Promise<unknown>)}))
vi.mock('@/lib/local-draft-seal',()=>{
 class LocalDraftReadRevoked extends Error {}
 return {LocalDraftReadRevoked,forgetLocalDraftPage(){},isSealedLocalDraft:()=>true,createLocalDraftCipher:async()=>{
  if(m.cipher)return m.cipher()
  if(m.revoked)throw new LocalDraftReadRevoked('revoked')
  return {seal:async(text:string)=>text,open:async(text:string)=>text,close(){}}
 }}
})
vi.mock('@/lib/artifact-drafts',async()=>{const actual=await vi.importActual<typeof import('@/lib/artifact-drafts')>('@/lib/artifact-drafts');return {...actual,purgeLocalDraftPage:(...args:Parameters<typeof actual.purgeLocalDraftPage>)=>{m.purge(...args);return actual.purgeLocalDraftPage(...args)}}})
vi.mock('@/hooks/use-auth',()=>({useSessionSafe:()=>({data:{user:{id:'owner'}}})}))
vi.mock('@/lib/api-fetch',()=>({tryRefresh:async()=>'ok',apiFetch:(url:string,init:RequestInit)=>m.fetch(url,init)}))
vi.mock('@/hooks/use-artifact-agent-dispatch',()=>({useArtifactAgentDispatch:()=>({dispatch:vi.fn(),ready:false})}))
vi.mock('@/components/features/chat/artifact-context/viewer/artifact-slides-preview',()=>({ArtifactSlidesPreview:()=>null}))
vi.mock('@/components/features/chat/artifact-context/viewer/artifact-dashboard-preview',()=>({ArtifactDashboardPreview:()=>null}))
const snapshot={page_id:'p',can_write:true,can_comment:true,can_share:false,can_restore:false,can_send_to_agent:false,latest_version:1,shared_version:1,manifest:{version:1,entrypoint:'index.html',files:[{path:'index.html',content:'<h1>Shared</h1>',media_type:'text/html'}]},versions:[{version:1}],quota:{used_bytes:1,limit_bytes:100,warning:false},comments:[]}
afterEach(()=>{cleanup();m.fetch.mockReset();m.purge.mockReset();m.revoked=false;m.cipher=undefined})
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
// Project scope of the draft rule (docs/specs/pages.md): proof erases, an ended
// sign-in or a dropped connection does not, and offline has no proof to obtain.
it.each([
 {name:'401: an ended sign-in is not asked for proof',fail:()=>new Response('{}',{status:401}),proof:true,erased:false},
 {name:'network failure (offline)',fail:()=>{throw new TypeError('Failed to fetch')},proof:true,erased:false},
 {name:'403 without proof',fail:()=>new Response('{}',{status:403}),proof:false,erased:false},
 {name:'403 with proof (read removed, membership removed)',fail:()=>new Response('{}',{status:403}),proof:true,erased:true},
 {name:'404 with proof (Page deleted)',fail:()=>new Response('{}',{status:404}),proof:true,erased:true},
])('Project draft on $name',async({fail,proof,erased})=>{
 let allowed=true
 m.fetch.mockImplementation(async()=>{if(allowed)return new Response(JSON.stringify(snapshot));return fail()})
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}})
 render(<QueryClientProvider client={client}><ArtifactProject workspaceId="w" slug="demo" /></QueryClientProvider>)
 const edit=await screen.findByRole('button',{name:'Edit source'})
 // It stays disabled until the kept draft is known.
 await waitFor(()=>expect(edit).toBeEnabled())
 fireEvent.click(edit)
 fireEvent.change(screen.getByLabelText('Source file'),{target:{value:'Private human source'}})
 allowed=false;m.revoked=proof
 await client.invalidateQueries({queryKey:['artifact-project','w','demo']})
 await screen.findByRole('alert')
 await new Promise(resolve=>setTimeout(resolve,50))
 expect(m.purge).toHaveBeenCalledTimes(erased?1:0)
 expect(Boolean(screen.queryByText(/^Page read access was removed/))).toBe(erased)
})
// A returning tab heard nothing while it was away, so a kept draft waits for a read
// asked after the return: "nejdřív ukážeme koncept a až potom zjistíme oprávnění".
describe('a tab coming back',()=>{
 const returns=[['visibilitychange',()=>document.dispatchEvent(new Event('visibilitychange'))],['pageshow',()=>window.dispatchEvent(new Event('pageshow'))],['online',()=>window.dispatchEvent(new Event('online'))]] as const
 let client:QueryClient|undefined
 async function typed(){
  m.fetch.mockImplementation(async()=>new Response(JSON.stringify(snapshot)))
  client=new QueryClient({defaultOptions:{queries:{retry:false}}})
  render(<QueryClientProvider client={client}><ArtifactProject workspaceId="w" slug="demo" /></QueryClientProvider>)
  const edit=await screen.findByRole('button',{name:'Edit source'})
  await waitFor(()=>expect(edit).toBeEnabled())
  fireEvent.click(edit)
  fireEvent.change(screen.getByLabelText('Source file'),{target:{value:'Private human source'}})
 }
 const screenRead=()=>client!.getQueryCache().getAll().map(q=>q.state.dataUpdatedAt).reduce((a,b)=>Math.max(a,b))
 const held=()=>{let answer!:(response:Response)=>void;m.fetch.mockImplementation(()=>new Promise<Response>(resolve=>{answer=resolve}));return (response:Response)=>answer(response)}
 it.each(returns)('%s takes the draft off screen before any answer, and keeps it',async(_,back)=>{
  await typed()
  const answer=held()
  act(()=>{back()})
  expect(screen.getByLabelText('Source file')).not.toBeVisible()
  await act(async()=>{answer(new Response(JSON.stringify(snapshot)))})
  await waitFor(()=>expect(screen.getByLabelText('Source file')).toBeVisible())
  expect(screen.getByLabelText('Source file')).toHaveValue('Private human source')
 })
 it.each([['in the same millisecond as the last read',0],['after the clock moved back',-60_000]])('a return %s still hides the draft until a read started after it answers',async(_,shift)=>{
  await typed()
  const answer=held()
  const moment=screenRead()+Number(shift)
  const clock=vi.spyOn(Date,'now').mockReturnValue(moment)
  try{
   act(()=>{window.dispatchEvent(new Event('pageshow'))})
   expect(screen.getByLabelText('Source file')).not.toBeVisible()
   await act(async()=>{answer(new Response(JSON.stringify(snapshot)))})
   await waitFor(()=>expect(screen.getByLabelText('Source file')).toBeVisible())
  }finally{clock.mockRestore()}
 })
 it('a slow return read keeps the focused field and its selection',async()=>{
  await typed()
  const box=screen.getByLabelText<HTMLTextAreaElement>('Source file')
  box.focus();box.setSelectionRange(2,5)
  const answer=held()
  act(()=>{window.dispatchEvent(new Event('pageshow'))})
  expect(box).not.toBeVisible()
  // jsdom keeps focus in a hidden element; a browser drops it. The browser proof is
  // the Project return-unchanged case of the draft-revocation suite.
  expect(box.closest('[hidden]')).toBeNull()
  expect(box.closest('[inert]')).not.toBeNull()
  await act(async()=>{answer(new Response(JSON.stringify(snapshot)))})
  await waitFor(()=>expect(box).toBeVisible())
  expect(screen.getByLabelText('Source file')).toBe(box)
  expect(document.activeElement).toBe(box)
  expect([box.selectionStart,box.selectionEnd]).toEqual([2,5])
 })
 it('a refused read offers the draft to nobody, even when the proof says Page read remains, and keeps it',async()=>{
  await typed()
  m.fetch.mockImplementation(async()=>new Response('{}',{status:403}))
  await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
  await screen.findByRole('alert')
  await act(async()=>{await Promise.resolve()})
  expect(screen.queryByLabelText('Source recovery draft')).toBeNull()
  expect(document.body.textContent).not.toContain('Private human source')
  expect(m.purge).not.toHaveBeenCalled()
 })
 describe('a return during the initial load',()=>{
  async function loading(){
   const answers:((response:Response)=>void)[]=[]
   m.fetch.mockImplementation((url:string)=>url.includes('/artifact')?new Promise<Response>(resolve=>{answers.push(resolve)}):Promise.resolve(new Response('{}')))
   client=new QueryClient({defaultOptions:{queries:{retry:false}}})
   render(<QueryClientProvider client={client}><ArtifactProject workspaceId="w" slug="demo" /></QueryClientProvider>)
   await waitFor(()=>expect(answers).toHaveLength(1))
   await act(async()=>{window.dispatchEvent(new Event('pageshow'))})
   await waitFor(()=>expect(answers).toHaveLength(2))
   return answers
  }
  it('takes nothing from the request started before it, whatever it says',async()=>{
   const answers=await loading()
   await act(async()=>{answers[0](new Response(JSON.stringify(snapshot)));await Promise.resolve()})
   expect(screen.queryByRole('button',{name:'Edit source'})).toBeNull()
   await act(async()=>{answers[1](new Response(JSON.stringify({...snapshot,can_write:false,can_comment:false})))})
   await screen.findByText('Read only')
   expect(screen.queryByRole('button',{name:'Edit source'})).toBeNull()
  })
  it('does not wait for a stalled request: its own read answers',async()=>{
   const answers=await loading()
   await act(async()=>{answers[1](new Response(JSON.stringify(snapshot)))})
   expect(await screen.findByRole('button',{name:'Edit source'})).toBeTruthy()
  })
 })
 it('a read started before a return answers nothing: only the read of that return counts, whatever order they answer in',async()=>{
  await typed()
  const answers:((response:Response)=>void)[]=[]
  m.fetch.mockImplementation(()=>new Promise<Response>(resolve=>{answers.push(resolve)}))
  act(()=>{window.dispatchEvent(new Event('pageshow'))})
  await waitFor(()=>expect(answers.length).toBeGreaterThan(0))
  const count=answers.length
  act(()=>{window.dispatchEvent(new Event('online'))})
  await waitFor(()=>expect(answers.length).toBeGreaterThan(count))
  await act(async()=>{answers[count-1](new Response(JSON.stringify(snapshot)))})
  expect(screen.getByLabelText('Source file')).not.toBeVisible()
  await act(async()=>{answers.at(-1)!(new Response(JSON.stringify(snapshot)))})
  await waitFor(()=>expect(screen.getByLabelText('Source file')).toBeVisible())
 })
 it('a read that never answers is asked again, with the draft off screen and the page saying why',async()=>{
  await typed()
  const asked=vi.fn(()=>new Promise<Response>(()=>{}))
  m.fetch.mockImplementation(asked)
  vi.useFakeTimers({toFake:['setInterval','clearInterval','setTimeout','clearTimeout']})
  try{
   act(()=>{window.dispatchEvent(new Event('pageshow'))})
   const first=asked.mock.calls.length
   await act(async()=>{await vi.advanceTimersByTimeAsync(1_100)})
   expect(screen.getByText('Checking access…')).toBeTruthy()
   await act(async()=>{await vi.advanceTimersByTimeAsync(10_000)})
   expect(asked.mock.calls.length).toBeGreaterThan(first)
   expect(screen.getByLabelText('Source file')).not.toBeVisible()
  }finally{vi.useRealTimers()}
 })
 it('a read that fails leaves it off screen until one succeeds',async()=>{
  await typed()
  m.fetch.mockImplementation(async()=>{throw new TypeError('Failed to fetch')})
  await act(async()=>{window.dispatchEvent(new Event('online'))})
  await screen.findByRole('alert')
  expect(screen.queryByLabelText('Source recovery draft')).toBeNull()
 })
})
