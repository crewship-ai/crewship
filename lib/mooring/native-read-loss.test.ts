import {beforeEach,describe,expect,it,vi} from "vitest"
const m=vi.hoisted(()=>({capture:vi.fn(),purge:vi.fn(),revoked:vi.fn()}))
vi.mock("../artifact-drafts",()=>({captureLocalDraftPage:m.capture,purgeLocalDraftPage:m.purge}))
vi.mock("../local-draft-seal",()=>({nativeDraftReadRevoked:m.revoked}))
import {verifyNativeReadLoss} from "./native-read-loss"
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
 it("keeps the draft when the editor verified read again before a late proof arrived",async()=>{
  m.revoked.mockResolvedValue(true)
  await expect(verifyNativeReadLoss(scope,async()=>false)).resolves.toBe(false);expect(m.purge).not.toHaveBeenCalled()
 })
 it("does not ask the server when this browser holds nothing",async()=>{
  m.capture.mockResolvedValue({local:[],artifacts:[],documents:[]})
  await expect(verifyNativeReadLoss(scope)).resolves.toBe(false);expect(m.revoked).not.toHaveBeenCalled()
 })
})
