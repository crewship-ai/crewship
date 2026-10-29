import {expect,it,vi} from 'vitest'
import {readNativePreview} from './native-preview-client'

const body=()=>({version:'2',bytes:btoa('{"shared":true}'),applicationId:'wordcraft',enginePin:'a'.repeat(64),editorBundle:'b'.repeat(64),editorURL:`https://native.example/b/${'b'.repeat(64)}/index.html`})
it('fetches only the published preview without admission, custody, or mutation',async()=>{
 const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify(body())))
 const signal=new AbortController().signal
 const result=await readNativePreview({workspaceId:'w',pageId:'p'},signal,fetch)
 expect(fetch).toHaveBeenCalledOnce()
 expect(fetch).toHaveBeenCalledWith('/api/v1/pages/%40p/craft/preview?workspace_id=w',{method:'GET',cache:'no-store',redirect:'error',signal})
 expect(result.version).toBe('2');expect(new TextDecoder().decode(result.bytes)).toBe('{"shared":true}')
})
it.each([403,404,503])('never falls back to latest or creates a session after %s',async status=>{
 const fetch=vi.fn().mockResolvedValue(new Response('{}',{status}))
 await expect(readNativePreview({workspaceId:'w',pageId:'p'},new AbortController().signal,fetch)).rejects.toThrow()
 expect(fetch).toHaveBeenCalledOnce()
})
it.each([{version:'02'},{version:'0'},{applicationId:'unknown'},{enginePin:'bad'},{bytes:'!!'},{bytes:''}])('refuses malformed immutable preview metadata %j',async invalid=>{
 const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify({...body(),...invalid})))
 await expect(readNativePreview({workspaceId:'w',pageId:'p'},new AbortController().signal,fetch)).rejects.toThrow()
})

it('preserves binary Deck preview bytes without treating ZIP as JSON',async()=>{
 const bytes=Uint8Array.from([80,75,3,4,0,255,128,0,10])
 const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify({...body(),applicationId:'deckcraft',bytes:btoa(String.fromCharCode(...bytes))})))
 const result=await readNativePreview({workspaceId:'w',pageId:'p'},new AbortController().signal,fetch)
 expect(result.applicationId).toBe('deckcraft');expect(result.bytes).toEqual(bytes)
 expect(fetch).toHaveBeenCalledOnce()
})
