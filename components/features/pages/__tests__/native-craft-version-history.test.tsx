import {render,screen,fireEvent,waitFor} from "@testing-library/react"
import {describe,it,expect,vi} from "vitest"
import {NativeCraftVersionHistory} from "../native-craft-version-history"
import type {NativeVersionList} from "@/lib/mooring/native-versions"
const list:NativeVersionList={currentVersion:"3",truncated:false,versions:[
 {version:"3",createdAt:"2026-10-31T10:00:00Z",author:{actorId:"user:private-id",kind:"human",displayName:"Pavel"},restoredFrom:"1",current:true},
 {version:"2",createdAt:"2026-10-31T09:00:00Z",author:{actorId:"agent:a",kind:"agent",displayName:"Drafting Agent"},current:false},
 {version:"1",createdAt:"2026-10-31T08:00:00Z",author:{actorId:"user:private-id",kind:"human",displayName:"Pavel"},current:false},
]}
async function opened(props:Partial<React.ComponentProps<typeof NativeCraftVersionHistory>>={}){
 const onRestore=vi.fn()
 render(<NativeCraftVersionHistory load={async()=>list} canRestore={false} onRestore={onRestore} {...props}/>)
 fireEvent.click(await screen.findByRole("button",{name:"Historie verzí (3)"}))
 return onRestore
}
describe("native Craft version history",()=>{
 it("lists versions newest first with authors and marks only the current one",async()=>{
  await opened()
  const items=screen.getAllByRole("listitem")
  expect(items.map(item=>item.querySelector("b")?.textContent)).toEqual(["Verze 3","Verze 2","Verze 1"])
  expect(items[0]).toHaveAttribute("aria-current","true")
  expect(items[1]).not.toHaveAttribute("aria-current")
  expect(screen.getAllByText("aktuální")).toHaveLength(1)
  expect(items[0]).toHaveTextContent("obnovena z verze 1")
  expect(items[1]).toHaveTextContent("Drafting Agent (agent)")
  expect(screen.queryByText(/private-id/)).not.toBeInTheDocument()
 })
 it("keeps restore disabled for a reader or a non-holder and explains why",async()=>{
  const onRestore=await opened({canRestore:false,restoreHint:"Pro obnovení verze převezměte úpravy."})
  expect(screen.getByText("Pro obnovení verze převezměte úpravy.")).toBeInTheDocument()
  const buttons=screen.getAllByRole("button",{name:"Obnovit"})
  expect(buttons).toHaveLength(2)
  for(const button of buttons){expect(button).toBeDisabled();fireEvent.click(button)}
  expect(onRestore).not.toHaveBeenCalled()
 })
 it("lets the holder restore an older version after confirming; the current one has no restore",async()=>{
  const onRestore=await opened({canRestore:true})
  const current=screen.getAllByRole("listitem")[0]
  expect(current.querySelector("button")).toBeNull()
  fireEvent.click(screen.getAllByRole("button",{name:"Obnovit"})[1])
  expect(screen.getByText(/Vznikne nová verze s obsahem verze 1\. Nic se nesmaže\./)).toBeInTheDocument()
  expect(onRestore).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button",{name:"Obnovit jako novou verzi"}))
  expect(onRestore).toHaveBeenCalledWith("1")
 })
 it("disables restore while another action is pending",async()=>{
  await opened({canRestore:true,pending:true})
  for(const button of screen.getAllByRole("button",{name:"Obnovit"}))expect(button).toBeDisabled()
 })
 it("renders nothing while the flag is off",async()=>{
  const load=vi.fn(async()=>null)
  const {container}=render(<NativeCraftVersionHistory load={load} canRestore/>)
  await waitFor(()=>expect(load).toHaveBeenCalled())
  expect(container).toBeEmptyDOMElement()
 })
 it("reloads when the refresh key changes",async()=>{
  const load=vi.fn(async()=>list)
  const {rerender}=render(<NativeCraftVersionHistory load={load} refreshKey="1" canRestore/>)
  await waitFor(()=>expect(load).toHaveBeenCalledTimes(1))
  rerender(<NativeCraftVersionHistory load={load} refreshKey="2" canRestore/>)
  await waitFor(()=>expect(load).toHaveBeenCalledTimes(2))
 })
})
