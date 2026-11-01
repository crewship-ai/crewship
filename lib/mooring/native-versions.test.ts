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
