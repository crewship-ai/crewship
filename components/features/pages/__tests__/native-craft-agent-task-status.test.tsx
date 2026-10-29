import {act,cleanup,render,screen} from '@testing-library/react'
import {afterEach,beforeEach,describe,expect,it,vi} from 'vitest'
import {NativeCraftAgentTaskStatus} from '../native-craft-agent-task-status'
const m=vi.hoisted(()=>({fetch:vi.fn()}))
vi.mock('@/lib/api-fetch',()=>({apiFetch:m.fetch,AUTH_EVENT:'unit-native-auth-retired'}))
const scope={workspaceId:'workspace',pageId:'page',humanId:'original-human',enabled:true}
const receipt={workspaceId:'workspace',pageId:'page',applicationId:'gridcraft',sessionId:'native-agent:run',phase:'committed',issuedBy:'crewship',durability:'node',version:'9007199254740995',candidateHash:'sha256:'+'a'.repeat(64),contentHash:'sha256:'+'a'.repeat(64)}
const payload=(overrides:Record<string,unknown>={})=>({work_id:'work',state:'running',agent_id:'agent',run_id:'run',application:'gridcraft',uncertain:false,operation_inflight:false,events:[],...overrides})
const response=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status})
const deferred=<T,>()=>{let resolve!:(value:T)=>void;const promise=new Promise<T>(r=>resolve=r);return {promise,resolve}}
const flush=async()=>{await act(async()=>{await Promise.resolve()})}
const tick=async()=>{await act(async()=>{await vi.advanceTimersByTimeAsync(2000)})}
beforeEach(()=>{vi.useFakeTimers();m.fetch.mockReset();window.history.replaceState(null,'','/?craft_task_id=work')})
afterEach(()=>{cleanup();vi.useRealTimers();window.history.replaceState(null,'','/')})
describe('native agent task private projection',()=>{
 it.each(['no-query','disabled','no-human'])('does not fetch without explicit enabled human task selection: %s',async(mode)=>{
  if(mode==='no-query')window.history.replaceState(null,'','/')
  render(<NativeCraftAgentTaskStatus {...scope} enabled={mode!=='disabled'} humanId={mode==='no-human'?'':scope.humanId}/>);await flush()
  expect(m.fetch).not.toHaveBeenCalled();expect(screen.queryByRole('complementary')).toBeNull()
 })
 it('is default-off when the optional enabled flag is absent',async()=>{
  render(<NativeCraftAgentTaskStatus workspaceId="workspace" pageId="page" humanId="original-human"/>);await flush();expect(m.fetch).not.toHaveBeenCalled()
 })
 it('fetches only the selected scoped task and displays exact Node receipt version',async()=>{
  m.fetch.mockResolvedValue(response(payload({last_committed_receipt:receipt})));render(<NativeCraftAgentTaskStatus {...scope}/>);await flush()
  expect(m.fetch).toHaveBeenCalledWith('/api/v1/pages/%40page/craft/tasks/work?workspace_id=workspace',expect.objectContaining({method:'GET',cache:'no-store',redirect:'error',skipRefresh:true,signal:expect.any(AbortSignal)}))
  expect(screen.getByText('Poslední potvrzená verze 9007199254740995')).toBeInTheDocument()
 })
 it.each([
  {name:'wrong work',body:payload({work_id:'other',last_committed_receipt:receipt})},
  {name:'wrong page',body:payload({last_committed_receipt:{...receipt,pageId:'other'}})},
  {name:'wrong workspace',body:payload({last_committed_receipt:{...receipt,workspaceId:'other'}})},
  {name:'wrong run',body:payload({last_committed_receipt:{...receipt,sessionId:'native-agent:other'}})},
  {name:'uncommitted',body:payload({last_committed_receipt:{...receipt,phase:'candidate'}})},
  {name:'non Node',body:payload({last_committed_receipt:{...receipt,durability:'memory'}})},
  {name:'hash mismatch',body:payload({last_committed_receipt:{...receipt,contentHash:'sha256:'+'b'.repeat(64)}})},
  {name:'numeric version',body:payload({last_committed_receipt:{...receipt,version:2}})},
 ])('refuses $name without saved-version claim',async({body})=>{
  m.fetch.mockResolvedValue(response(body));render(<NativeCraftAgentTaskStatus {...scope}/>);await flush()
  expect(screen.getByRole('alert')).toBeInTheDocument();expect(screen.queryByText(/Poslední potvrzená verze/)).toBeNull()
 })
 it('describes actual human acquire refusal in past tense and clears it after a later acquire succeeds',async()=>{
  m.fetch.mockResolvedValue(response(payload({events:[{operation:'acquire',status:409,holder_kind:'human'}]})));render(<NativeCraftAgentTaskStatus {...scope}/>);await flush()
  expect(screen.getByText(/Agent nepřevzal úpravy/)).toBeInTheDocument();expect(screen.queryByText(/právě upravuje/)).toBeNull()
  m.fetch.mockResolvedValue(response(payload({events:[{operation:'acquire',status:409,holder_kind:'human'},{operation:'acquire',status:200}]})));await tick()
  expect(screen.queryByText(/nepřevzal|nemůže upravovat/)).toBeNull()
 })
 it.each([{operation:'read',status:409,holder_kind:'human'},{operation:'acquire',status:409,holder_kind:'agent'},{operation:'acquire',status:409}])('does not invent human refusal from $operation/$holder_kind',async(event)=>{
  m.fetch.mockResolvedValue(response(payload({events:[event]})));render(<NativeCraftAgentTaskStatus {...scope}/>);await flush()
  expect(screen.queryByText(/člověk|nemůže|nepřevzal/)).toBeNull()
 })
 it('auth retirement hides private state and an ignored abort response cannot restore it',async()=>{
  m.fetch.mockResolvedValueOnce(response(payload({last_committed_receipt:receipt})));const old=deferred<Response>();m.fetch.mockReturnValueOnce(old.promise)
  render(<NativeCraftAgentTaskStatus {...scope}/>);await flush();expect(screen.getByText(/9007199254740995/)).toBeInTheDocument();await tick()
  act(()=>window.dispatchEvent(new Event('unit-native-auth-retired')));expect(screen.queryByRole('complementary')).toBeNull()
  await act(async()=>old.resolve(response(payload({last_committed_receipt:receipt}))));await tick();expect(screen.queryByText(/9007199254740995/)).toBeNull();expect(m.fetch).toHaveBeenCalledTimes(2)
 })
 it.each([401,403])('denial %s retires polling and later success cannot resurrect the private view',async(status)=>{
  m.fetch.mockResolvedValueOnce(response(payload({last_committed_receipt:receipt}))).mockResolvedValueOnce(response({},status)).mockResolvedValue(response(payload({last_committed_receipt:receipt})))
  render(<NativeCraftAgentTaskStatus {...scope}/>);await flush();expect(screen.getByText(/9007199254740995/)).toBeInTheDocument();await tick();expect(screen.queryByText(/9007199254740995/)).toBeNull();await tick()
  expect(m.fetch).toHaveBeenCalledTimes(2);expect(screen.queryByText(/9007199254740995/)).toBeNull()
 })
 it.each(['pageId','workspaceId','humanId'] as const)('scope change %s hides old state and ignores its delayed response',async(field)=>{
  const old=deferred<Response>(),fresh=deferred<Response>();m.fetch.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise)
  const view=render(<NativeCraftAgentTaskStatus {...scope}/>);view.rerender(<NativeCraftAgentTaskStatus {...scope} {...{[field]:'other'}}/>);expect(screen.queryByRole('complementary')).toBeNull()
  const freshReceipt={...receipt,workspaceId:field==='workspaceId'?'other':scope.workspaceId,pageId:field==='pageId'?'other':scope.pageId,version:'2'}
  await act(async()=>fresh.resolve(response(payload({last_committed_receipt:freshReceipt}))));expect(screen.getByText('Poslední potvrzená verze 2')).toBeInTheDocument()
  await act(async()=>old.resolve(response(payload({last_committed_receipt:receipt}))));expect(screen.queryByText(/9007199254740995/)).toBeNull();expect(screen.getByText('Poslední potvrzená verze 2')).toBeInTheDocument()
 })
 it('a newly selected work query cannot keep the old private task on rerender',async()=>{
  m.fetch.mockResolvedValueOnce(response(payload({last_committed_receipt:receipt})));const next=deferred<Response>();m.fetch.mockReturnValueOnce(next.promise)
  const view=render(<NativeCraftAgentTaskStatus {...scope}/>);await flush();expect(screen.getByText(/9007199254740995/)).toBeInTheDocument()
  window.history.replaceState(null,'','/?craft_task_id=other');view.rerender(<NativeCraftAgentTaskStatus {...scope}/>);expect(screen.queryByRole('complementary')).toBeNull();expect(m.fetch).toHaveBeenCalledTimes(2)
 })
 it('may retry a transient network failure while hiding the old private result',async()=>{
  m.fetch.mockResolvedValueOnce(response(payload({last_committed_receipt:receipt}))).mockRejectedValueOnce(new TypeError('network')).mockResolvedValue(response(payload({last_committed_receipt:{...receipt,version:'2'}})))
  render(<NativeCraftAgentTaskStatus {...scope}/>);await flush();await tick();expect(screen.getByRole('alert')).toBeInTheDocument();expect(screen.queryByText(/9007199254740995/)).toBeNull();await tick();expect(screen.getByText('Poslední potvrzená verze 2')).toBeInTheDocument()
 })
 it('re-enabling the same scope cannot resurrect its pre-disconnection private receipt',async()=>{
  m.fetch.mockResolvedValueOnce(response(payload({last_committed_receipt:receipt}))).mockReturnValue(new Promise<Response>(()=>{}))
  const view=render(<NativeCraftAgentTaskStatus {...scope}/>);await flush();expect(screen.getByText(/9007199254740995/)).toBeInTheDocument()
  view.rerender(<NativeCraftAgentTaskStatus {...scope} enabled={false}/>);view.rerender(<NativeCraftAgentTaskStatus {...scope}/>);expect(screen.queryByRole('complementary')).toBeNull()
 })
 it('returning to human scope A after B requires fresh authorization before displaying cached A data',async()=>{
  m.fetch.mockResolvedValueOnce(response(payload({last_committed_receipt:receipt}))).mockReturnValue(new Promise<Response>(()=>{}))
  const view=render(<NativeCraftAgentTaskStatus {...scope}/>);await flush();expect(screen.getByText(/9007199254740995/)).toBeInTheDocument()
  view.rerender(<NativeCraftAgentTaskStatus {...scope} humanId="other-human"/>);view.rerender(<NativeCraftAgentTaskStatus {...scope}/>);expect(screen.queryByRole('complementary')).toBeNull()
 })
 it('disabled connection immediately hides private state and ignores delayed old response',async()=>{
  m.fetch.mockResolvedValueOnce(response(payload({last_committed_receipt:receipt})));const old=deferred<Response>();m.fetch.mockReturnValueOnce(old.promise)
  const view=render(<NativeCraftAgentTaskStatus {...scope}/>);await flush();await tick();view.rerender(<NativeCraftAgentTaskStatus {...scope} enabled={false}/>);expect(screen.queryByRole('complementary')).toBeNull()
  await act(async()=>old.resolve(response(payload({last_committed_receipt:receipt}))));expect(screen.queryByText(/9007199254740995/)).toBeNull()
 })
})
