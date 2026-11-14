import {webcrypto} from "node:crypto"
import {render,waitFor,act} from "@testing-library/react"
import {beforeEach,afterEach,describe,it,expect,vi} from "vitest"
import type {NativeEditorPortOptions} from "@/lib/mooring/native-editor-port"
import {NativeCraftEditorHost} from "../native-craft-editor-host"

// The real native port and the real host window listener: only the server and
// the draft journal are stubbed. The sandboxed editor frame always speaks with
// the opaque origin "null"; its hello must reach the port (#5134 regression).
const m=vi.hoisted(()=>({apiFetch:vi.fn(),createClient:vi.fn(),openJournal:vi.fn(),createPort:vi.fn(),portError:vi.fn(),handled:vi.fn(),registerPrivacy:vi.fn()}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:m.apiFetch,AUTH_EVENT:"unit-native-auth-retired"}))
vi.mock("../native-craft-agent-start",()=>({NativeCraftAgentStart:()=>null}))
vi.mock("@/lib/mooring/native-document-client",()=>({createNativeDocumentClient:m.createClient}))
vi.mock("@/lib/mooring/native-editor-port",async()=>{const actual=await vi.importActual<typeof import("@/lib/mooring/native-editor-port")>("@/lib/mooring/native-editor-port");return {...actual,createNativeEditorPort:(o:NativeEditorPortOptions)=>{m.createPort(o);const port=actual.createNativeEditorPort({...o,onError(reason){m.portError(reason);o.onError(reason)}});const handle=port.handleWindowMessage.bind(port);port.handleWindowMessage=(event)=>{m.handled(event);return handle(event)};return port}}})
vi.mock("@/lib/mooring/native-draft",async()=>({...await vi.importActual("@/lib/mooring/native-draft"),openNativeDraftJournal:m.openJournal}))
vi.mock("@/lib/document-queue",async()=>({...await vi.importActual("@/lib/document-queue"),registerDocumentDraftWriter:m.registerPrivacy,purgeDocumentDrafts:vi.fn()}))
vi.mock("../workbook-editor-host",()=>({editorFrameSupported:()=>true,createEditorFrame:(doc:Document,src:string,onLoad:()=>void)=>{const f=doc.createElement('iframe');f.setAttribute('sandbox','allow-scripts');f.setAttribute('credentialless','');f.setAttribute('referrerpolicy','no-referrer');f.srcdoc='';f.src=src;queueMicrotask(onLoad);return f}}))

const bundle="b".repeat(64)
const emptyLease={can_edit:true,can_share:true,holder:null,mine:false,generation:null,can_write:false,acquired_at:null,expires_at:null,handoff_requested_by:[]}
function clientRig(app:string){return {open:vi.fn().mockResolvedValue({sessionId:"private-session",applicationId:app}),read:vi.fn().mockResolvedValue({version:"1",bytes:new TextEncoder().encode('{"body":[]}'),applicationId:app,enginePin:"a".repeat(64),editorBundle:bundle,editorURL:`https://native.example/b/${bundle}/index.html`,quota:{usedBytes:"12",limitBytes:"99999",warning:false,retention:"keep-all"}}),lease:vi.fn().mockResolvedValue(emptyLease),leaseAction:vi.fn(),events:vi.fn(()=>new Promise<void>(()=>{})),reauthorize:vi.fn().mockResolvedValue(true),save:vi.fn(),reconcile:vi.fn(),close:vi.fn(),proposals:vi.fn().mockResolvedValue([]),proposalBytes:vi.fn(),decideProposal:vi.fn(),share:vi.fn()}}
const journal={persist:vi.fn(),confirm:vi.fn(),discardRecovered:vi.fn(),recovery:vi.fn().mockResolvedValue([]),onRetire:vi.fn(()=>()=>{}),close:vi.fn().mockResolvedValue(undefined),revokeRead:vi.fn(),eraseOwnRow:vi.fn()}

beforeEach(()=>{vi.stubGlobal('crypto',webcrypto);m.registerPrivacy.mockReturnValue(vi.fn());m.openJournal.mockResolvedValue(journal)})
afterEach(()=>{vi.unstubAllGlobals();vi.clearAllMocks()})

async function mountFrame(app:"wordcraft"|"gridcraft"){
 m.createClient.mockReturnValue(clientRig(app))
 const rendered=render(<NativeCraftEditorHost workspaceId="w" pageId="p" humanId="human" enabled />)
 await waitFor(()=>expect(m.createPort).toHaveBeenCalledOnce())
 const frame=rendered.container.ownerDocument.querySelector('iframe')!
 const target=frame.contentWindow!
 const post=vi.spyOn(target,'postMessage').mockImplementation(()=>{})
 // The frame's load event has reached the port (loads === 1) before any hello.
 await act(async()=>{await Promise.resolve()})
 const hello=(source:unknown,origin:string,type=`${app}:hello`)=>act(()=>{window.dispatchEvent(new MessageEvent("message",{source:source as Window,origin,data:{type,protocol:1,bundle}}))})
 const connects=()=>post.mock.calls.filter(c=>(c[0] as {type?:string})?.type===`${app}:connect`)
 return {rendered,frame,target,post,hello,connects}
}

describe.each(['wordcraft','gridcraft'] as const)('%s editor hello through the real host listener',(app)=>{
 it('hands the private channel to the sandboxed frame on its opaque-origin hello',async()=>{
  const {target,hello,connects}=await mountFrame(app)
  await hello(target,"null")
  expect(connects()).toHaveLength(1)
  const [message,targetOrigin,transfer]=connects()[0] as unknown as [Record<string,unknown>,string,unknown[]]
  expect(message).toEqual({type:`${app}:connect`,protocol:1,bundle})
  expect(targetOrigin).toBe("*")
  expect(transfer).toHaveLength(1)
 })
 it('ignores an opaque-origin hello from another window and still connects the frame',async()=>{
  const {rendered,target,hello,connects}=await mountFrame(app)
  const other=rendered.container.ownerDocument.createElement('iframe');rendered.container.append(other)
  await hello(other.contentWindow,"null")
  await hello(window,"null")
  expect(connects()).toHaveLength(0)
  // The host's own filter, not only the port's re-check, keeps foreign windows out.
  expect(m.handled).not.toHaveBeenCalled()
  await hello(target,"null")
  expect(connects()).toHaveLength(1)
 })
 it('fails closed on a hello from the frame window with a non-opaque origin',async()=>{
  const {target,hello,connects}=await mountFrame(app)
  await hello(target,"https://native.example")
  expect(connects()).toHaveLength(0)
  await hello(target,"null")
  expect(connects()).toHaveLength(0)
  expect(m.portError).toHaveBeenCalledWith('native_editor_handshake')
 })
 it('ignores a message without a source while the frame has no window',async()=>{
  const {frame,hello,connects}=await mountFrame(app)
  Object.defineProperty(frame,'contentWindow',{configurable:true,get:()=>null})
  await hello(null,"null")
  expect(m.handled).not.toHaveBeenCalled()
  expect(connects()).toHaveLength(0)
  expect(m.portError).not.toHaveBeenCalled()
 })
 it('does not handshake again on a second hello',async()=>{
  const {target,hello,connects}=await mountFrame(app)
  await hello(target,"null")
  await hello(target,"null")
  expect(connects()).toHaveLength(1)
 })
})
