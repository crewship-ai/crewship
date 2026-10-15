import { act,cleanup,fireEvent,render,screen,waitFor } from '@testing-library/react'
import { afterEach,beforeEach,it,expect,vi } from 'vitest'
import { ArtifactCreationPolicyCard } from '../artifact-creation-policy-card'
import { apiFetch } from '@/lib/api-fetch'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
vi.mock('@/lib/api-fetch',()=>({apiFetch:vi.fn()}))
let client: QueryClient
let version=0,enabled=false,ids:string[]=[],fail=0,readFail=0
const writes:Record<string,unknown>[]=[]
beforeEach(()=>{
 cleanup();client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});vi.mocked(apiFetch).mockClear();version=0;enabled=false;ids=[];fail=0;readFail=0;writes.length=0
 vi.mocked(apiFetch).mockImplementation(async(url,init)=>{
  if(init?.method==='PUT'){
   const input=JSON.parse(String(init.body));writes.push(input)
   if(fail)return new Response(JSON.stringify({error:fail===409?'Policy changed':'Permission lost'}),{status:fail})
   version++;enabled=input.enabled;ids=input.crew_ids
  }
  if(!init?.method && readFail && String(url).includes('/artifact-creation-policy?')) return new Response(JSON.stringify({error:'Policy read unavailable'}),{status:readFail})
  if(String(url).includes('/crews?'))return new Response(JSON.stringify([{id:'crew1',name:'Design'},{id:'crew2',name:'Reports'}]))
  return new Response(JSON.stringify({enabled,crew_ids:ids,version}))
 })
})
it('starts disabled and saves explicit workspace switch plus selected crew using original version',async()=>{
 render(<QueryClientProvider client={client}><ArtifactCreationPolicyCard workspaceId="ws" role="ADMIN"/></QueryClientProvider>)
 const toggle=await screen.findByRole('switch',{name:'Agents may create artifacts'})
 expect(toggle).not.toBeChecked();expect(writes).toHaveLength(0)
 fireEvent.click(screen.getByLabelText('Allow artifacts from Design'));fireEvent.click(toggle)
 fireEvent.click(screen.getByRole('button',{name:'Save policy'}))
 await waitFor(()=>expect(writes).toHaveLength(1))
 expect(writes[0]).toEqual({enabled:true,crew_ids:['crew1'],base_version:0})
})
it('retains a stale policy draft until explicit discard/reload and purges private allowlist on denial',async()=>{
 render(<QueryClientProvider client={client}><ArtifactCreationPolicyCard workspaceId="ws" role="OWNER"/></QueryClientProvider>)
 fireEvent.click(await screen.findByLabelText('Allow artifacts from Design'));fireEvent.click(screen.getByRole('switch'))
 fail=409;fireEvent.click(screen.getByRole('button',{name:'Save policy'}));await screen.findByText('Policy changed')
 expect(screen.getByLabelText('Allow artifacts from Design')).toBeChecked();expect(screen.getByRole('switch')).toBeChecked()
 fireEvent.click(screen.getByRole('button',{name:'Review latest policy'}));expect(screen.getByRole('alertdialog')).toBeInTheDocument()
 fireEvent.click(screen.getByRole('button',{name:'Keep draft'}));expect(screen.getByRole('switch')).toBeChecked()
 fail=403;fireEvent.click(screen.getByRole('button',{name:'Save policy'}))
 await waitFor(()=>expect(screen.queryByText('Design')).toBeNull());expect(screen.queryByRole('switch')).toBeNull()
})
it('does not request or display admin policy for non-admin members',()=>{
 render(<QueryClientProvider client={client}><ArtifactCreationPolicyCard workspaceId="ws" role="MEMBER"/></QueryClientProvider>)
 expect(apiFetch).not.toHaveBeenCalled();expect(screen.queryByRole('switch')).toBeNull()
})

afterEach(()=>{cleanup();client.clear()})

it('preserves drafts through background reads without advancing CAS and clears denied cache before a fresh retry',async()=>{
 render(<QueryClientProvider client={client}><ArtifactCreationPolicyCard workspaceId="ws" role="OWNER"/></QueryClientProvider>)
 fireEvent.click(await screen.findByLabelText('Allow artifacts from Design'))
 fireEvent.click(screen.getByRole('switch'))
 version=7;ids=['crew2'];enabled=false
 await act(async()=>{await client.invalidateQueries({queryKey:['artifact-creation-policy','ws']})})
 expect(screen.getByLabelText('Allow artifacts from Design')).toBeChecked()
 fireEvent.click(screen.getByRole('button',{name:'Cancel'}))
 fireEvent.click(screen.getByLabelText('Allow artifacts from Design'));fireEvent.click(screen.getByRole('switch'))
 fail=409;fireEvent.click(screen.getByRole('button',{name:'Save policy'}));await screen.findByText('Policy changed')
 expect(writes[0].base_version).toBe(0)
 readFail=500
 await act(async()=>{await client.invalidateQueries({queryKey:['artifact-creation-policy','ws']})})
 expect(await screen.findByRole('alert')).toHaveTextContent('Policy read unavailable')
 expect(screen.getByLabelText('Allow artifacts from Design')).toBeChecked()
 readFail=403
 await act(async()=>{await client.invalidateQueries({queryKey:['artifact-creation-policy','ws']})})
 await waitFor(()=>expect(screen.queryByRole('switch')).toBeNull())
 expect(client.getQueriesData({queryKey:['artifact-creation-policy','ws']}).every(([,data])=>data===undefined)).toBe(true)
 readFail=0;fail=0
 fireEvent.click(screen.getByRole('button',{name:'Retry'}))
 expect(await screen.findByLabelText('Allow artifacts from Reports')).toBeChecked()
 expect(screen.getByLabelText('Allow artifacts from Design')).not.toBeChecked()
 fireEvent.click(screen.getByRole('switch'));fireEvent.click(screen.getByRole('button',{name:'Save policy'}))
 await waitFor(()=>expect(writes).toHaveLength(2))
 expect(writes[1].base_version).toBe(7)
})
