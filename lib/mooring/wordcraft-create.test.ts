import { afterEach, expect, it, vi } from "vitest"
import { createEmptyWordCraftCheckpoint } from "./wordcraft-create"
import { WORDCRAFT_EMPTY_NATIVE } from "./wordcraft-empty"
const mocks=vi.hoisted(()=>({load:vi.fn(),model:vi.fn(),free:vi.fn(),readonly:false}))
vi.mock("./wordcraft-host-session",()=>({loadWordCraftReceiptSDK:mocks.load}))
afterEach(()=>{vi.clearAllMocks();mocks.readonly=false})
function prepare(){
 mocks.model.mockImplementation(function(){return {is_read_only:()=>mocks.readonly,checkpoint:()=>new Uint8Array([1,2,255]),free:mocks.free}})
 mocks.load.mockResolvedValue({WordCraftModel:mocks.model})
}
it("uses the original canonical bytes and one fresh stable paragraph identity",async()=>{
 prepare()
 await expect(createEmptyWordCraftCheckpoint("/release-prefix")).resolves.toBe("AQL/")
 expect(mocks.model.mock.calls[0][0]).toBe(WORDCRAFT_EMPTY_NATIVE)
 expect(JSON.parse(mocks.model.mock.calls[0][1])).toEqual([expect.any(String)])
 expect(mocks.free).toHaveBeenCalledTimes(1)
})
it("refuses a read-only blank instead of creating an unusable document",async()=>{
 prepare();mocks.readonly=true
 await expect(createEmptyWordCraftCheckpoint("/release-prefix")).rejects.toThrow("cannot be edited")
 expect(mocks.free).toHaveBeenCalledTimes(1)
})
