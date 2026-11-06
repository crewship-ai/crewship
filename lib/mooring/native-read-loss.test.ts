import {beforeEach,describe,expect,it,vi} from "vitest"
const m=vi.hoisted(()=>({capture:vi.fn(),purge:vi.fn(),revoked:vi.fn(),forget:vi.fn()}))
vi.mock("../artifact-drafts",()=>({captureLocalDraftPage:m.capture,purgeLocalDraftPage:m.purge}))
vi.mock("../local-draft-seal",()=>({nativeDraftReadRevoked:m.revoked,forgetLocalDraftPage:m.forget}))
import {confirmNativeRead,verifyNativeReadLoss} from "./native-read-loss"
const scope={human:"h",workspace:"w",page:"p"},held={local:[],artifacts:[],documents:[{key:["row"]}]}
beforeEach(()=>{vi.clearAllMocks();m.capture.mockResolvedValue(held);m.purge.mockResolvedValue(undefined)})
describe("verifyNativeReadLoss",()=>{
 it("erases only what it captured, after retiring the editor, on draft_read_revoked",async()=>{
  m.revoked.mockResolvedValue(true);const retire=vi.fn().mockResolvedValue(undefined)
  await expect(verifyNativeReadLoss(scope,retire)).resolves.toBe(true)
  expect(retire.mock.invocationCallOrder[0]).toBeLessThan(m.purge.mock.invocationCallOrder[0])
  expect(m.purge).toHaveBeenCalledWith(scope,held)
 })
 it.each([["a 401, 503 or network failure",()=>Promise.reject(new Error("unavailable"))],["no proof",()=>Promise.resolve(false)]])("keeps the draft on %s",async(_,answer)=>{
  m.revoked.mockImplementation(answer)
  await expect(verifyNativeReadLoss(scope)).resolves.toBe(false);expect(m.purge).not.toHaveBeenCalled()
 })
 it("a read confirmed after asking supersedes the proof: nothing closes, retires or erases",async()=>{
  let answer!:(value:boolean)=>void
  m.revoked.mockReturnValue(new Promise<boolean>(resolve=>{answer=resolve}));const retire=vi.fn()
  const check=verifyNativeReadLoss(scope,retire)
  await vi.waitFor(()=>expect(m.revoked).toHaveBeenCalled())
  confirmNativeRead(scope);answer(true)
  await expect(check).resolves.toBe(false)
  expect(m.forget).not.toHaveBeenCalled();expect(retire).not.toHaveBeenCalled();expect(m.purge).not.toHaveBeenCalled()
 })
 it("a confirmation for another Page does not supersede",async()=>{
  m.revoked.mockResolvedValue(true);const check=verifyNativeReadLoss(scope)
  confirmNativeRead({...scope,page:"other"})
  await expect(check).resolves.toBe(true);expect(m.forget).toHaveBeenCalledWith(scope)
 })
 it("does not ask the server when this browser holds nothing",async()=>{
  m.capture.mockResolvedValue({local:[],artifacts:[],documents:[]})
  await expect(verifyNativeReadLoss(scope)).resolves.toBe(false);expect(m.revoked).not.toHaveBeenCalled()
 })
})
