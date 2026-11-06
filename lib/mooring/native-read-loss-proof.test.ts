import {beforeEach,describe,expect,it,vi} from "vitest"
const m=vi.hoisted(()=>({capture:vi.fn(),purge:vi.fn(),fetch:vi.fn()}))
vi.mock("../artifact-drafts",()=>({captureLocalDraftPage:m.capture,purgeLocalDraftPage:m.purge}))
vi.mock("../api-fetch",async()=>({...await vi.importActual("../api-fetch"),apiFetch:m.fetch}))
import {verifyNativeReadLoss} from "./native-read-loss"
const scope={human:"h",workspace:"w",page:"proof"},held={local:[],artifacts:[],documents:[{key:["row"]}]}
const revoked=()=>new Response(JSON.stringify({code:"draft_read_revoked"}),{status:403}),readable=()=>new Response(JSON.stringify({readable:true}),{status:200})
beforeEach(()=>{vi.clearAllMocks();m.capture.mockResolvedValue(held);m.purge.mockResolvedValue(undefined)})
describe("draft-key answers against each other",()=>{
 it("a readable answer supersedes an older proof still on the wire",async()=>{
  let arrive!:(response:Response)=>void
  m.fetch.mockReturnValueOnce(new Promise<Response>(resolve=>{arrive=resolve}))
  const older=verifyNativeReadLoss(scope)
  await vi.waitFor(()=>expect(m.fetch).toHaveBeenCalledTimes(1))
  m.fetch.mockResolvedValueOnce(readable())
  await expect(verifyNativeReadLoss(scope)).resolves.toBe(false)
  arrive(revoked())
  await expect(older).resolves.toBe(false)
  expect(m.purge).not.toHaveBeenCalled()
 })
 it.each([["a 503",()=>new Response("{}",{status:503})],["a 200 that does not say readable",()=>new Response("{}",{status:200})]])("%s is no confirmation: the older proof still erases",async(_,answer)=>{
  let arrive!:(response:Response)=>void
  m.fetch.mockReturnValueOnce(new Promise<Response>(resolve=>{arrive=resolve}))
  const older=verifyNativeReadLoss({...scope,page:"proof-"+_})
  await vi.waitFor(()=>expect(m.fetch).toHaveBeenCalledTimes(1))
  m.fetch.mockResolvedValueOnce(answer())
  await expect(verifyNativeReadLoss({...scope,page:"proof-"+_})).resolves.toBe(false)
  arrive(revoked())
  await expect(older).resolves.toBe(true)
  expect(m.purge).toHaveBeenCalledOnce()
 })
})
