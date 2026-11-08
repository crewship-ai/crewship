import {render,screen,waitFor,act} from '@testing-library/react'
import {beforeEach,afterEach,it,expect,vi} from 'vitest'
import {APP_ROUTE_PATTERNS,isKnownAppPath} from '@/lib/app-routes'
import CraftPage from '@/app/(dashboard)/pages/craft/editor/page'
const m=vi.hoisted(()=>({readLoss:vi.fn(),host:vi.fn(),preview:vi.fn(),fetch:vi.fn(),params:new URLSearchParams('workspace_id=w&page_id=p'),workspace:{workspaceId:'w',loading:false,linkUnavailable:false},session:{status:'authenticated',data:{user:{id:'human'}}}}))
vi.mock('@/lib/mooring/native-read-loss',()=>({verifyNativeReadLoss:m.readLoss}))
const nav=vi.hoisted(()=>({replace:vi.fn()}))
vi.mock('next/navigation',()=>({useSearchParams:()=>m.params,useRouter:()=>({replace:nav.replace,push:vi.fn()})}))
vi.mock('@/hooks/use-auth',()=>({useSession:()=>m.session}))
vi.mock('@/hooks/use-page-workspace',()=>({usePageWorkspace:()=>m.workspace}))
vi.mock('@/lib/api-fetch',()=>({apiFetch:m.fetch}))
const r=vi.hoisted(()=>({access:'trusted' as string}))
vi.mock('@/hooks/use-access-mode',()=>({useAccessMode:()=>r.access}))
vi.mock('@/components/features/artifacts-library/legacy-pages-redirect',()=>({LegacyPagesRedirect:({target,children}:{target:{reference:string;workspaceId:string};children:unknown})=><div data-testid="resolve" data-reference={target.reference} data-ws={target.workspaceId}>{children as never}</div>}))
vi.mock('../native-craft-editor-host',()=>({NativeCraftEditorHost:(props:unknown)=>{m.host(props);return <div>Full native Word</div>}}))
vi.mock('../native-craft-preview',()=>({NativeCraftPreview:(props:unknown)=>{m.preview(props);return <div>Shared native preview</div>}}))
const deferred=<T,>()=>{let resolve!:(v:T)=>void;const promise=new Promise<T>(r=>resolve=r);return {promise,resolve}}
const availability=(enabled:boolean)=>new Response(JSON.stringify({enabled,can_edit:enabled}),{status:200,headers:{'content-type':'application/json'}})
beforeEach(()=>{m.readLoss.mockReset();m.readLoss.mockResolvedValue(false);vi.stubEnv('NEXT_PUBLIC_CRAFT_NATIVE_ENABLED','false');m.host.mockClear();m.preview.mockClear();m.fetch.mockReset();m.fetch.mockResolvedValue(availability(true));m.params=new URLSearchParams('workspace_id=w&page_id=p');m.workspace={workspaceId:'w',loading:false,linkUnavailable:false};m.session={status:'authenticated',data:{user:{id:'human'}}}})
afterEach(()=>vi.unstubAllEnvs())
// Availability cannot say why a Page is closed: the draft leaves only on proof, and an open Page never asks.
it('asks for proof of read loss only when neither editor nor preview is available',async()=>{
 m.fetch.mockResolvedValue(availability(false));const {unmount}=render(<CraftPage/>)
 await waitFor(()=>expect(m.readLoss).toHaveBeenCalledOnce());expect(m.readLoss.mock.calls[0][0]).toMatchObject({workspace:'w',page:'p'});unmount()
 m.readLoss.mockClear();m.fetch.mockResolvedValue(availability(true));render(<CraftPage/>);await screen.findByText('Full native Word');expect(m.readLoss).not.toHaveBeenCalled()
})
it.each([
 ['a preview-only answer',()=>Promise.resolve(new Response(JSON.stringify({enabled:false,can_edit:false,preview_enabled:true}),{status:200}))],
 ['a refused availability',()=>Promise.resolve(new Response(null,{status:403}))],
 ['a failed availability',()=>Promise.reject(new TypeError('Failed to fetch'))],
])('asks for proof after %s',async(_,answer)=>{
 m.fetch.mockImplementation(answer);render(<CraftPage/>)
 await waitFor(()=>expect(m.readLoss).toHaveBeenCalledOnce());expect(m.readLoss.mock.calls[0][0]).toMatchObject({human:expect.any(String),workspace:'w',page:'p'})
})
it('asks for proof when the workspace left the directory, from the link\'s own scope',async()=>{
 m.workspace={workspaceId:'',loading:false,linkUnavailable:true}
 try{render(<CraftPage/>);await waitFor(()=>expect(m.readLoss).toHaveBeenCalledOnce());expect(m.readLoss.mock.calls[0][0]).toMatchObject({workspace:'w',page:'p'});expect(m.fetch).not.toHaveBeenCalled()}
 finally{m.workspace={workspaceId:'w',loading:false,linkUnavailable:false}}
})
it('mounts one full host only after authenticated page-scoped server availability even with no build flag',async()=>{render(<CraftPage/>);expect(m.host).not.toHaveBeenCalled();await screen.findByText('Full native Word');expect(m.fetch).toHaveBeenCalledWith('/api/v1/pages/%40p/craft/availability?workspace_id=w',expect.objectContaining({signal:expect.any(AbortSignal),cache:'no-store'}));expect(m.host).toHaveBeenCalledWith(expect.objectContaining({workspaceId:'w',pageId:'p',humanId:'human',enabled:true}))})
it('keeps the server default false off even if an old frontend build flag is true',async()=>{vi.stubEnv('NEXT_PUBLIC_CRAFT_NATIVE_ENABLED','true');m.fetch.mockResolvedValue(availability(false));render(<CraftPage/>);await screen.findByText('The native editor is not turned on.');expect(m.fetch).toHaveBeenCalledOnce();expect(m.host).not.toHaveBeenCalled()})
it('never mounts or admits a host after an unavailable or forbidden availability response',async()=>{m.fetch.mockResolvedValue(new Response(null,{status:403}));render(<CraftPage/>);await screen.findByRole('alert');expect(m.host).not.toHaveBeenCalled()})
it('aborts and ignores a stale enabled result after a page scope change whose availability is forbidden',async()=>{const first=deferred<Response>();m.fetch.mockReturnValueOnce(first.promise).mockResolvedValueOnce(new Response(null,{status:403}));const mounted=render(<CraftPage/>);await waitFor(()=>expect(m.fetch).toHaveBeenCalledOnce());const signal=m.fetch.mock.calls[0][1].signal as AbortSignal;m.params=new URLSearchParams('workspace_id=w&page_id=q');mounted.rerender(<CraftPage/>);await screen.findByRole('alert');expect(signal.aborted).toBe(true);await act(async()=>first.resolve(availability(true)));expect(m.host).not.toHaveBeenCalled();expect(m.fetch.mock.calls[1][0]).toBe('/api/v1/pages/%40q/craft/availability?workspace_id=w')})
it('immediately unmounts the old host while a different human scope is pending or refused',async()=>{const second=deferred<Response>();m.fetch.mockResolvedValueOnce(availability(true)).mockReturnValueOnce(second.promise);const mounted=render(<CraftPage/>);await screen.findByText('Full native Word');m.session={status:'authenticated',data:{user:{id:'another-human'}}};mounted.rerender(<CraftPage/>);expect(screen.queryByText('Full native Word')).not.toBeInTheDocument();await act(async()=>second.resolve(new Response(null,{status:403})));await screen.findByRole('alert');expect(screen.queryByText('Full native Word')).not.toBeInTheDocument()})
it.each(['workspace_id=w&page_id=p&page_id=other','workspace_id=other&page_id=p','workspace_id=w','workspace_id=w&workspace_id=other&page_id=p','workspace_id=w&page_id=%00'])('refuses ambiguous or mismatched deep links before availability %s',(query)=>{m.params=new URLSearchParams(query);render(<CraftPage/>);expect(m.fetch).not.toHaveBeenCalled();expect(m.host).not.toHaveBeenCalled();expect(screen.getByRole('alert')).toBeInTheDocument()})
it('cannot take human identity from query parameters or an unauthenticated session',()=>{m.session={status:'unauthenticated',data:{user:{id:'human'}}};m.params.set('human_id','foreign');render(<CraftPage/>);expect(m.fetch).not.toHaveBeenCalled();expect(m.host).not.toHaveBeenCalled()})
it('leaves the existing craft Page slug route available and reserves only the new three-segment editor entry',()=>{expect(APP_ROUTE_PATTERNS).not.toContain('/pages/craft');expect(APP_ROUTE_PATTERNS).toContain('/pages/craft/editor');expect(isKnownAppPath('/pages/craft')).toBe(true);expect(isKnownAppPath('/pages/craft/editor')).toBe(true);expect(isKnownAppPath('/pages/craft/editor/unexpected')).toBe(false)})

it('routes Comment to shared preview only when the server explicitly permits it',async()=>{
 m.fetch.mockResolvedValue(new Response(JSON.stringify({enabled:false,preview_enabled:true})))
 render(<CraftPage/>);await screen.findByText('Shared native preview')
 expect(m.host).not.toHaveBeenCalled();expect(m.preview).toHaveBeenCalledWith(expect.objectContaining({humanId:'human',workspaceId:'w',pageId:'p'}))
})
it('does not infer preview permission from a truthy nonboolean field',async()=>{
 m.fetch.mockResolvedValue(new Response(JSON.stringify({enabled:false,preview_enabled:'true'})))
 render(<CraftPage/>);await screen.findByText('The native editor is not turned on.')
 expect(m.host).not.toHaveBeenCalled();expect(m.preview).not.toHaveBeenCalled()
})

it.each([false,undefined,'true'])('routes a legacy enabled observer with can_edit=%s only to shared preview',async(canEdit)=>{
 m.fetch.mockResolvedValue(new Response(JSON.stringify({enabled:true,can_edit:canEdit,preview_enabled:true})))
 render(<CraftPage/>);await screen.findByText('Shared native preview')
 expect(m.host).not.toHaveBeenCalled();expect(m.preview).toHaveBeenCalled()
})
it('does not open either surface when legacy enabled lacks explicit edit and preview permission',async()=>{
 m.fetch.mockResolvedValue(new Response(JSON.stringify({enabled:true})))
 render(<CraftPage/>);await screen.findByText('The native editor is not turned on.')
 expect(m.host).not.toHaveBeenCalled();expect(m.preview).not.toHaveBeenCalled()
})

// #5129: with the library on, a trusted session opens the document at its
// Artifacts detail; a restricted session and the library-off build keep this route.
it('with the library on resolves a trusted session to the Artifacts detail, keeping this editor as the fallback',async()=>{
 vi.stubEnv('NEXT_PUBLIC_ARTIFACTS_LIBRARY','true');r.access='trusted'
 render(<CraftPage/>)
 expect(screen.getByTestId('resolve').dataset).toMatchObject({reference:'@p',ws:'w'})
 await screen.findByText('Full native Word')
})
it('keeps a trusted session with the library off on this route',async()=>{
 vi.stubEnv('NEXT_PUBLIC_ARTIFACTS_LIBRARY','false');r.access='trusted'
 render(<CraftPage/>)
 expect(screen.queryByTestId('resolve')).toBeNull()
 await screen.findByText('Full native Word')
})
// #5132: a restricted member's surface is /artifacts; the old editor address
// is replaced there and the native editor never mounts.
it.each(['true','false'])('replaces a restricted session at the old editor address with /artifacts (library %s)',async(library)=>{
 vi.stubEnv('NEXT_PUBLIC_ARTIFACTS_LIBRARY',library);r.access='restricted';nav.replace.mockClear()
 window.history.replaceState(null,'','/pages/craft/editor?page_id=p')
 render(<CraftPage/>)
 await vi.waitFor(()=>expect(nav.replace).toHaveBeenCalledWith('/artifacts/p'))
 expect(screen.queryByTestId('resolve')).toBeNull()
 expect(m.host).not.toHaveBeenCalled()
})
