import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react'
import {afterEach,expect,it,vi} from 'vitest'
import {NewDocumentDialog} from '../new-document-dialog'
import {emptyGridCraftNativeDocument} from '@/lib/mooring/gridcraft-empty'
const mocks=vi.hoisted(()=>({fetch:vi.fn(),sheet:true as unknown,word:false,legacy:false,allowed:true}))
vi.mock('@/lib/api-fetch',()=>({apiFetch:mocks.fetch,broadcastSessionExpired:vi.fn()}))
vi.mock('@/hooks/use-page-creation-access',()=>({usePageCreationAccess:()=>mocks.allowed}))
vi.mock('@/hooks/use-documents-availability',()=>({useDocumentsAvailability:()=>({data:mocks.legacy,wordcraftNative:mocks.word,gridcraftNative:mocks.sheet})}))
afterEach(()=>{cleanup();mocks.fetch.mockReset();mocks.sheet=true;mocks.word=false;mocks.legacy=false;mocks.allowed=true})
const response=(value:unknown,status=200)=>Promise.resolve({ok:status<400,status,json:async()=>value})
function prepare(overrides:{initialize?:unknown;release?:'lost'|'expired';availability?:number}={}){
 mocks.fetch.mockImplementation((url:string)=>url.includes('/craft/initialize')?response(overrides.initialize??{application_id:'gridcraft',page_id:'sheet-page',session_token:'a'.repeat(64),generation:'9007199254740993',version:'1'},201):url.includes('/lease/release')?overrides.release==='lost'?Promise.reject(Error('lost response')):response({},overrides.release==='expired'?409:200):url.includes('/craft/availability')?response({enabled:true},overrides.availability??200):response({id:'sheet-page',slug:'budget'},201))
 const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0},mutations:{retry:false}}}),created=vi.fn()
 render(<QueryClientProvider client={client}><NewDocumentDialog workspaceId='workspace' onClose={vi.fn()} onCreated={created}/></QueryClientProvider>)
 fireEvent.change(screen.getByLabelText('Name'),{target:{value:'Budget'}})
 return {client,created}
}
it('creates a qualified whole-native Sheet with canonical engine bytes and releases its exact creation custody',async()=>{
 const {client,created}=prepare()
 expect(screen.getByRole('option',{name:'Sheet'})).toBeInTheDocument()
 expect(screen.getByLabelText('Format')).toHaveValue('sheet')
 fireEvent.click(screen.getByRole('button',{name:'Create Doc'}))
 await waitFor(()=>expect(created).toHaveBeenCalledWith('budget','sheet-page'))
 const initialize=mocks.fetch.mock.calls.find(([url])=>url.includes('/craft/initialize'))!
 const body=JSON.parse(initialize[1].body)
 expect(body.application_id).toBe('gridcraft')
 expect(Uint8Array.from(atob(body.bytes),(c:string)=>c.charCodeAt(0))).toEqual(emptyGridCraftNativeDocument())
 const release=mocks.fetch.mock.calls.find(([url])=>url.includes('/lease/release'))!
 expect(JSON.parse(release[1].body)).toEqual({generation:'9007199254740993'})
 expect(release[1].headers['X-Craft-Session']).toBe('a'.repeat(64))
 expect(initialize[1]).toMatchObject({redirect:'error',skipRefresh:true})
 expect(mocks.fetch.mock.calls[0][1].body).not.toContain('owner')
 client.clear()
})
it('offers Sheet explicitly beside Word and honors that selection without initializing Word',async()=>{
 mocks.word=true;const {client,created}=prepare()
 expect(screen.getByLabelText('Format')).toHaveValue('native')
 fireEvent.change(screen.getByLabelText('Format'),{target:{value:'sheet'}})
 fireEvent.click(screen.getByRole('button',{name:'Create Doc'}))
 await waitFor(()=>expect(created).toHaveBeenCalledWith('budget','sheet-page'))
 expect(JSON.parse(mocks.fetch.mock.calls.find(([url])=>url.includes('/craft/initialize'))![1].body).application_id).toBe('gridcraft')
 client.clear()
})
it.each([false,'true',undefined,null])('does not offer an unconfirmed Sheet capability %s',(capability)=>{
 mocks.sheet=capability;const {client}=prepare()
 expect(screen.queryByRole('option',{name:'Sheet'})).toBeNull()
 expect(screen.getByRole('button',{name:'Create Doc'})).toBeDisabled()
 expect(mocks.fetch).not.toHaveBeenCalled();client.clear()
})
it.each(['lost','expired'] as const)('opens the same confirmed Sheet read-only after %s creation release',async(release)=>{
 const {client,created}=prepare({release})
 fireEvent.click(screen.getByRole('button',{name:'Create Doc'}))
 await waitFor(()=>expect(created).toHaveBeenCalledWith('budget','sheet-page'))
 expect(mocks.fetch.mock.calls.filter(([url])=>url.includes('/craft/initialize'))).toHaveLength(1)
 expect(mocks.fetch.mock.calls.filter(([url])=>url.includes('/craft/availability'))).toHaveLength(1)
 client.clear()
})
it.each([{}, {application_id:'wordcraft',page_id:'sheet-page',session_token:'a'.repeat(64),generation:'1',version:'1'}])('never repeats ambiguous or wrong-profile initialization',async(initialize)=>{
 const {client,created}=prepare({initialize})
 fireEvent.click(screen.getByRole('button',{name:'Create Doc'}));await screen.findByRole('alert')
 fireEvent.click(screen.getByRole('button',{name:'Retry initialization'}))
 await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('could not be confirmed'))
 expect(mocks.fetch.mock.calls.filter(([url])=>url.includes('/craft/initialize'))).toHaveLength(1)
 expect(mocks.fetch.mock.calls.filter(([url])=>url.includes('/lease/release'))).toHaveLength(0)
 expect(mocks.fetch.mock.calls.filter(([url])=>!url.includes('/craft/'))).toHaveLength(1)
 expect(created).not.toHaveBeenCalled();client.clear()
})
