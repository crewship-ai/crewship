import { act, cleanup, renderHook } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
const state=vi.hoisted(()=>({options:null as unknown as {onOwnMessageSaved:(text:string,metadata:unknown)=>void},send:vi.fn((_text:string,_metadata:Record<string,unknown>)=>true),connected:true}))
vi.mock("@tanstack/react-query",()=>({useQuery:()=>({data:true,isError:false})}))
vi.mock("@/hooks/use-auth",()=>({useSessionSafe:()=>({data:{user:{id:"owner"}}})}))
vi.mock("@/lib/server-base",()=>({resolveWsBase:()=>"ws://localhost"}))
vi.mock("@/hooks/use-chat",()=>({useChat:(options:typeof state.options)=>{state.options=options;return {sendMessage:state.send,connectionStatus:state.connected?"connected":"disconnected",isStreaming:false,markHistoryUnavailable}}}))
const markHistoryUnavailable=()=>{}
import { useArtifactAgentDispatch } from "../use-artifact-agent-dispatch"
afterEach(()=>{cleanup();vi.clearAllMocks();state.connected=true})
const comment={id:"comment-1",version:2,path:"details.html",selector:"#heading",quote:"Old heading",text:"Change heading"}
it("sends exact page/comment/version scope and confirms only the persisted chat echo",async()=>{
 const hook=renderHook(()=>useArtifactAgentDispatch("workspace","page",{chat_id:"chat",agent_id:"agent"},true))
 let result:Promise<boolean>
 act(()=>{result=hook.result.current.dispatch(comment)})
 const metadata=state.send.mock.calls[0][1]
 expect(metadata).toEqual({artifact_project_intent:{submission_id:"artifact-comment-comment-1",page_id:"page",comment_id:"comment-1",base_version:2}})
 await act(async()=>{state.options.onOwnMessageSaved("saved",metadata);expect(await result!).toBe(true)})
})
it("retains the queued task if there is no source chat or live connection",async()=>{
 state.connected=false
 const hook=renderHook(()=>useArtifactAgentDispatch("workspace","page",{chat_id:"chat",agent_id:"agent"},true))
 expect(await hook.result.current.dispatch(comment)).toBe(false)
 expect(state.send).not.toHaveBeenCalled()
})
it("dispatches Design edits with an exact Design intent and Design tools",async()=>{
 const hook=renderHook(()=>useArtifactAgentDispatch("workspace","page",{chat_id:"chat",agent_id:"agent"},true,"design"))
 let result:Promise<boolean>
 act(()=>{result=hook.result.current.dispatch({...comment,path:"design.json",selector:"frame-cart"})})
 const [prompt,metadata]=state.send.mock.calls[0]
 expect(prompt).toContain("design_apply")
 expect(prompt).toContain("design_snapshot")
 expect(metadata).toEqual({artifact_project_intent:{kind:"design",submission_id:"artifact-comment-comment-1",page_id:"page",comment_id:"comment-1",base_version:2}})
 await act(async()=>{state.options.onOwnMessageSaved("saved",metadata);expect(await result!).toBe(true)})
})
