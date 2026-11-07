// @vitest-environment node
import {describe,it,expect,vi} from "vitest"
import {createNativeDocumentClient} from "./native-document-client"
import {NativeSaveError} from "./single-editor-save"
import {parseNativeVersionList} from "./native-versions"
const signal=()=>new AbortController().signal
const admission=()=>new Response(JSON.stringify({session_token:"b".repeat(64),session_id:"public-session",application_id:"wordcraft",page_id:"page"}))
const listing={currentVersion:"9007199254740995",truncated:false,versions:[
 {version:"9007199254740995",createdAt:"2026-10-31T10:00:00Z",author:{actorId:"user:u",kind:"human",displayName:"Pavel"},restoredFrom:"9007199254740993",current:true},
 {version:"9007199254740994",createdAt:"2026-10-31T09:00:00Z",author:{actorId:"agent:a",kind:"agent",displayName:"Drafting Agent"},current:false},
 {version:"9007199254740993",createdAt:"2026-10-31T08:00:00Z",author:{actorId:"user:u",kind:"human",displayName:"Pavel"},current:false},
]}
const restore={workspaceId:"ws",pageId:"page",applicationId:"wordcraft",enginePin:"e".repeat(64),sessionId:"public-session",generation:"7",baseVersion:"9007199254740995",targetVersion:"9007199254740993"}
describe("native version history adapter",()=>{
 it("accepts only a newest-first listing whose single current entry is the head",()=>{
  expect(parseNativeVersionList(listing)).toEqual(listing)
  expect(parseNativeVersionList({...listing,versions:[...listing.versions].reverse()})).toBeNull()
  expect(parseNativeVersionList({...listing,versions:listing.versions.map(v=>({...v,current:true}))})).toBeNull()
  expect(parseNativeVersionList({...listing,currentVersion:"1"})).toBeNull()
  expect(parseNativeVersionList({...listing,versions:[{...listing.versions[0],author:{...listing.versions[0].author,kind:"robot"}}]})).toBeNull()
  expect(parseNativeVersionList({...listing,versions:[]})).toBeNull()
 })
 it("lists under the private admission and treats 404 as history switched off",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify(listing))).mockResolvedValueOnce(new Response('{"error":"off"}',{status:404}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.versions(signal())).resolves.toEqual(listing)
  expect(String(send.mock.calls[1][0])).toBe("/api/v1/pages/%40page/craft/document/versions?workspace_id=ws")
  expect(new Headers(send.mock.calls[1][1].headers).get("X-Craft-Session")).toBe("b".repeat(64))
  await expect(client.versions(signal())).resolves.toBeNull()
 })
 it("restores through the fenced route with the exact binding and requires base+1",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify({version:"9007199254740996",durability:"node",issuedBy:"crewship",phase:"committed"})))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.restore(restore,signal())).resolves.toMatchObject({version:"9007199254740996"})
  expect(String(send.mock.calls[1][0])).toBe("/api/v1/pages/%40page/craft/document/restore?workspace_id=ws")
  expect(JSON.parse(send.mock.calls[1][1].body)).toEqual(restore)
  await expect(client.restore({...restore,sessionId:"other"},signal())).rejects.toThrow("invalid_native_restore_binding")
  expect(send).toHaveBeenCalledTimes(2)
 })
 it("reports a lease or base conflict and refuses a receipt that skipped a version",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response('{}',{status:409})).mockResolvedValueOnce(new Response(JSON.stringify({version:"9007199254740999",durability:"node",issuedBy:"crewship",phase:"committed"})))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.restore(restore,signal())).rejects.toBeInstanceOf(NativeSaveError)
  await expect(client.restore(restore,signal())).rejects.toThrow("native_restore_unconfirmed")
 })
})
describe("version history flag",()=>{
 it("is off unless defined, and a workspace override wins",async()=>{
  const {nativeFlagEnabled}=await import("./native-versions")
  expect(nativeFlagEnabled([],"craft_version_history")).toBe(false)
  expect(nativeFlagEnabled([{key:"craft_version_history",enabled:true}],"craft_version_history")).toBe(true)
  expect(nativeFlagEnabled([{key:"craft_version_history",enabled:true,override_enabled:false}],"craft_version_history")).toBe(false)
  expect(nativeFlagEnabled({},"craft_version_history")).toBe(false)
 })
})
describe("historical version read",()=>{
 const doc=(over:Record<string,unknown>={})=>({version:"2",currentVersion:"3",current:false,createdAt:"2026-10-31T09:00:00Z",author:{actorId:"agent:a",kind:"agent",displayName:"Drafting Agent"},applicationId:"wordcraft",enginePin:"e".repeat(64),editorBundle:"c".repeat(64),editorURL:"/_editor/b/"+"c".repeat(64)+"/index.html",bytes:btoa('{"v":2}'),...over})
 it("reads one version under the admission and decodes its bytes",async()=>{
  const {parseNativeVersionDocument}=await import("./native-versions")
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response(JSON.stringify(doc())))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  const got=await client.version("2",signal())
  expect(new TextDecoder().decode(got.bytes)).toBe('{"v":2}')
  expect(got).toMatchObject({version:"2",currentVersion:"3",current:false})
  expect(String(send.mock.calls[1][0])).toBe("/api/v1/pages/%40page/craft/document/versions/2?workspace_id=ws")
  expect(send.mock.calls[1][1].method).toBe("GET")
  expect(new Headers(send.mock.calls[1][1].headers).get("X-Craft-Session")).toBe("b".repeat(64))
  await expect(client.version("0",signal())).rejects.toThrow("invalid_native_version")
  await expect(client.version("../restore",signal())).rejects.toThrow("invalid_native_version")
  expect(send).toHaveBeenCalledTimes(2)
  // Anything but exactly the requested, self-consistent version is refused.
  expect(parseNativeVersionDocument(doc(),"1")).toBeNull()
  expect(parseNativeVersionDocument(doc({current:true}),"2")).toBeNull()
  expect(parseNativeVersionDocument(doc({currentVersion:"1"}),"2")).toBeNull()
  expect(parseNativeVersionDocument(doc({bytes:"not base64!"}),"2")).toBeNull()
  expect(parseNativeVersionDocument(doc({bytes:""}),"2")).toBeNull()
  expect(parseNativeVersionDocument(doc({editorURL:"https://evil.example/x.html"}),"2")).toBeNull()
  expect(parseNativeVersionDocument(doc({applicationId:"design"}),"2")).toBeNull()
 })
 it("fails closed on a refusal",async()=>{
  const send=vi.fn().mockResolvedValueOnce(admission()).mockResolvedValueOnce(new Response('{}',{status:403}))
  const client=createNativeDocumentClient({workspaceId:"ws",pageId:"page",fetch:send});await client.open(signal())
  await expect(client.version("2",signal())).rejects.toThrow("native_version_unavailable")
 })
})
describe("version comparison",()=>{
 it("diffs pretty-printed JSON line by line and keeps common lines",async()=>{
  const {nativeLineDiff,nativeVersionText}=await import("./native-versions")
  const before=nativeVersionText(new TextEncoder().encode('{"a":1,"b":2,"c":3}'))
  const after=nativeVersionText(new TextEncoder().encode('{"a":1,"b":5,"c":3,"d":4}'))
  const lines=nativeLineDiff(before,after)!
  expect(lines.filter(l=>l.kind==="removed").map(l=>l.text.trim())).toEqual(['"b": 2,','"c": 3'])
  expect(lines.filter(l=>l.kind==="added").map(l=>l.text.trim())).toEqual(['"b": 5,','"c": 3,','"d": 4'])
  expect(lines.filter(l=>l.kind==="same").map(l=>l.text.trim())).toEqual(["{",'"a": 1,',"}"])
  expect(nativeLineDiff("x","x")).toEqual([{kind:"same",text:"x"}])
  expect(nativeLineDiff(Array.from({length:3000},(_,i)=>"a"+i).join("\n"),Array.from({length:3000},(_,i)=>"b"+i).join("\n"))).toBeNull()
 })
 it("shows non-JSON bytes as text and refuses invalid UTF-8",async()=>{
  const {nativeVersionText}=await import("./native-versions")
  expect(nativeVersionText(new TextEncoder().encode("plain"))).toBe("plain")
  expect(()=>nativeVersionText(new Uint8Array([0xff,0xfe]))).toThrow()
 })
})
