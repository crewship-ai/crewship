import { expect, it } from "vitest"
import { pageOwnershipListSchema } from "./page-ownership"

const row = {id:"private-page",slug:"private-page",name:"Private output",owner_user_id:"owner",owner_name:"Owner",eligible_successors:[]}
it.each(["artifact", "board", "document", "slides"])("accepts the exact Go custody type %s", type => {
  expect(pageOwnershipListSchema.parse({pages:[{...row,type}],next_cursor:"opaque"})).toEqual({pages:[{...row,type}],next_cursor:"opaque"})
})
it("preserves a mixed list and rejects unknown output types",()=>{
  const pages=["artifact","board","document","slides"].map((type,index)=>({...row,id:`page-${index}`,type}))
  expect(pageOwnershipListSchema.parse({pages}).pages).toEqual(pages)
  expect(pageOwnershipListSchema.safeParse({pages:[{...row,type:"unknown-output"}]}).success).toBe(false)
})
