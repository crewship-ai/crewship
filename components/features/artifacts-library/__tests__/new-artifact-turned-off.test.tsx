import {cleanup,fireEvent,render,renderHook,screen,waitFor} from '@testing-library/react'
import {QueryClient,QueryClientProvider} from '@tanstack/react-query'
import {afterEach,expect,it,vi} from 'vitest'
import {useDocumentsAvailability} from '@/hooks/use-documents-availability'
import {NewArtifactButton,newArtifactReasons,TURNED_OFF,type NewArtifactPrefs} from '../new-artifact-button'

const m=vi.hoisted(()=>({fetch:vi.fn()}))
vi.mock('@/lib/api-fetch',()=>({apiFetch:m.fetch}))

// #5381: a Word or Sheet the instance administrator turned off (or is
// draining) says so in the New artifact menu, not "not available yet".
afterEach(cleanup)
const prefs:NewArtifactPrefs={pins:['docs','word'],recent:[],toggle:()=>'unavailable',move:()=>{},reset:()=>{},record:()=>{},guide:false,setGuide:()=>{}}

it.each([
 ['draining',{wordcraft:'draining',gridcraft:'open'},{word:TURNED_OFF}],
 ['closed',{wordcraft:'open',gridcraft:'closed'},{sheet:TURNED_OFF}],
 ['open',{wordcraft:'open',gridcraft:'open'},{}],
 ['unknown',{},{}],
])('maps a %s application to its reason',(_,modes,want)=>{
 expect(newArtifactReasons(modes)).toEqual(want)
})

it('says an administrator turned Word off, on the row and when it is chosen',()=>{
 render(<NewArtifactButton entries={[{kind:'docs',flow:'docs'}]} prefs={prefs} onNew={vi.fn()} open onOpenChange={()=>{}}
  reasons={newArtifactReasons({wordcraft:'draining'})}/>)
 const word=screen.getByRole('button',{name:'New Word'})
 expect(word.getAttribute('title')).toBe('Turned off on this server by an administrator')
 fireEvent.click(word)
 expect(screen.getByRole('status').textContent).toBe('Word: turned off on this server by an administrator')
})

it('keeps "not available yet" without an administrator reason',()=>{
 render(<NewArtifactButton entries={[{kind:'docs',flow:'docs'}]} prefs={prefs} onNew={vi.fn()} open onOpenChange={()=>{}}/>)
 expect(screen.getByRole('button',{name:'New Word'}).getAttribute('title')).toBe('Not available in this workspace yet')
})

it('reads each application mode from the documents status, and nothing it does not know',async()=>{
 m.fetch.mockResolvedValue(new Response(JSON.stringify({enabled:true,applications:{wordcraft:'draining',gridcraft:'sideways',agents:'open'}})))
 const client=new QueryClient()
 const {result}=renderHook(()=>useDocumentsAvailability('ws1'),{wrapper:({children})=><QueryClientProvider client={client}>{children}</QueryClientProvider>})
 await waitFor(()=>expect(result.current.applications).toEqual({wordcraft:'draining'}))
})
