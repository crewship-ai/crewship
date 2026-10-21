import { describe, expect, it } from "vitest"
import { verifyInitialization, verifyMutationReceipt } from "./contracts"

describe("Document acknowledgments", () => {
  const attempt = { id: "attempt", epoch: "opaque", schema: 3, baselineRev: 4 }
  it("accepts an exact retry receipt against its original baseline after later edits", () => {
    expect(verifyMutationReceipt({id:"attempt",epoch:"opaque",rev:5,duplicate:true}, attempt).rev).toBe(5)
  })
  it.each([{id:"other",epoch:"opaque",rev:5},{id:"attempt",epoch:"old",rev:5},{id:"attempt",epoch:"opaque",rev:3},{id:"attempt",epoch:"opaque",rev:4},{id:"attempt",epoch:"opaque",rev:NaN},{id:"attempt",epoch:"opaque",rev:5,schema:2}])("rejects an unbound or stale receipt %j", receipt => {
    expect(()=>verifyMutationReceipt(receipt,attempt)).toThrow(/confirmed/i)
  })
  it("accepts numeric first initialization state and numeric exact-retry receipt", () => {
    expect(()=>verifyInitialization({schema:3,rev:0,epoch:1,generation:1,incarnation:"generation",snapshot:"AAA=",snapshotRev:0,updates:[],blocks:[]},"attempt")).not.toThrow()
    expect(()=>verifyInitialization({id:"attempt",rev:0,epoch:1,actor:"human",kind:"init",found:true,duplicate:true},"attempt")).not.toThrow()
  })
  it.each([{id:"other",rev:0,epoch:1,actor:"human",kind:"init",found:true,duplicate:true},{schema:3,rev:0,epoch:"opaque"},null])("rejects invalid initialization acknowledgment %j", value => {
    expect(()=>verifyInitialization(value,"attempt")).toThrow()
  })
})
