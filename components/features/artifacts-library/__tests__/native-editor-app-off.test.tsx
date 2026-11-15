import {cleanup,render,screen} from '@testing-library/react'
import {afterEach,beforeEach,expect,it,vi} from 'vitest'
import {ArtifactNativeEditor} from '../native-editor'

// #5381: while an administrator has Word or Sheet draining or off, the editor
// opens read-only, says why, and never mounts the editing host.
const m=vi.hoisted(()=>({fetch:vi.fn(),readLoss:vi.fn()}))
vi.mock('@/hooks/use-auth',()=>({useSessionSafe:()=>({status:'authenticated',data:{user:{id:'human'}}})}))
vi.mock('@/lib/api-fetch',()=>({apiFetch:m.fetch}))
vi.mock('@/lib/mooring/native-read-loss',()=>({verifyNativeReadLoss:m.readLoss}))
vi.mock('@/components/features/pages/native-craft-editor-host',()=>({NativeCraftEditorHost:()=><div>host</div>}))
vi.mock('@/components/features/pages/native-craft-preview',()=>({NativeCraftPreview:()=><div>preview</div>}))
vi.mock('@/components/features/pages/native-craft-reader-history',()=>({NativeCraftReaderHistory:()=>null}))
const answer=(value:unknown)=>m.fetch.mockImplementation(()=>Promise.resolve(new Response(JSON.stringify(value))))
beforeEach(()=>{m.readLoss.mockResolvedValue(false)})
afterEach(()=>{cleanup();vi.clearAllMocks()})
const WORD_OFF='An administrator turned Word off on this server. You can read this document; editing is not available.'
const SHEET_OFF='An administrator turned Sheet off on this server. You can read this document; editing is not available.'

it.each([
 ['Word draining',{application:'wordcraft',edit_refusal:'app_draining',applications:{wordcraft:'draining',gridcraft:'open'}},WORD_OFF],
 ['Word off',{application:'wordcraft',edit_refusal:'app_disabled',applications:{wordcraft:'closed',gridcraft:'open'}},WORD_OFF],
 ['Sheet off',{application:'gridcraft',edit_refusal:'app_disabled',applications:{wordcraft:'open',gridcraft:'closed'}},SHEET_OFF],
])('%s: read-only with the administrator reason, no editor and no shared-only preview',async(_,fields,text)=>{
 answer({enabled:true,can_edit:false,preview_enabled:true,agents_enabled:false,...fields})
 render(<ArtifactNativeEditor workspaceId="w" pageId="p" fallback={<div>saved copy</div>}/>)
 expect(await screen.findByText(text)).toBeTruthy()
 expect(screen.getByText('saved copy')).toBeTruthy()
 expect(screen.queryByText('host')).toBeNull()
 expect(screen.queryByText('preview')).toBeNull()
})

it('never mounts the editor when an answer still says can_edit while the application drains',async()=>{
 answer({enabled:true,can_edit:true,preview_enabled:true,application:'wordcraft',applications:{wordcraft:'draining',gridcraft:'open'}})
 render(<ArtifactNativeEditor workspaceId="w" pageId="p" fallback={null}/>)
 expect(await screen.findByText(WORD_OFF)).toBeTruthy()
 expect(screen.queryByText('host')).toBeNull()
})

it('opens the editor when only the other application is off',async()=>{
 answer({enabled:true,can_edit:true,preview_enabled:true,application:'wordcraft',applications:{wordcraft:'open',gridcraft:'closed'}})
 render(<ArtifactNativeEditor workspaceId="w" pageId="p" fallback={null}/>)
 expect(await screen.findByText('host')).toBeTruthy()
})

it('keeps the plain read-only text when this person has no Edit',async()=>{
 answer({enabled:true,can_edit:false,preview_enabled:false,application:'wordcraft',applications:{wordcraft:'closed',gridcraft:'open'}})
 render(<ArtifactNativeEditor workspaceId="w" pageId="p" fallback={null}/>)
 expect(await screen.findByText('You can read this document but not edit it.')).toBeTruthy()
})
