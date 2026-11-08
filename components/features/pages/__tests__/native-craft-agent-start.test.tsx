import {act,cleanup,fireEvent,render,screen,waitFor} from "@testing-library/react"
import {afterEach,beforeEach,describe,expect,it,vi} from "vitest"

// The real chat client runs here: only its socket is replaced, so the message
// id the task binds to comes from the same persisted echo the product receives.
const transport=vi.hoisted(()=>({send:vi.fn(),connected:true,onMessage:undefined as undefined|((message:unknown)=>void)}))
const m=vi.hoisted(()=>({fetch:vi.fn()}))
vi.mock("@/hooks/use-websocket",async importOriginal=>({
 ...await importOriginal<typeof import("@/hooks/use-websocket")>(),
 useWebSocket:(options:{enabled?:boolean;onMessage?:(message:unknown)=>void})=>{transport.onMessage=options.onMessage;return {status:options.enabled&&transport.connected?"connected":"disconnected",send:transport.send,retry:vi.fn()}},
}))
vi.mock("@/lib/server-base",()=>({resolveWsBase:()=>"ws://t"}))
vi.mock("@/lib/api-fetch",async importOriginal=>({...await importOriginal<typeof import("@/lib/api-fetch")>(),apiFetch:m.fetch}))
vi.mock("@/hooks/use-workspace-agent-directory",()=>({useWorkspaceAgentDirectory:(workspace?:string)=>({agents:workspace?[{id:"agent-1",slug:"jana",name:"Jana"}]:null,error:false})}))
import {NativeCraftAgentStart} from "../native-craft-agent-start"

const json=(body:unknown,status=200)=>new Response(JSON.stringify(body),{status})
const props={workspaceId:"w",pageId:"p",humanId:"human",enabled:true,onStarted:vi.fn()}
let availability:()=>Response,profile:()=>Response,task:()=>Response
const calls=(part:string)=>m.fetch.mock.calls.filter(([url])=>String(url).includes(part))
const taskCalls=()=>calls("/craft/tasks")
const start=()=>screen.getByRole("button",{name:"Start agent"})
async function openForm(){fireEvent.click(await screen.findByRole("button",{name:"Edit with an agent"}));fireEvent.change(screen.getByRole("combobox",{name:"Agent"}),{target:{value:"agent-1"}});fireEvent.change(screen.getByRole("textbox",{name:"Instruction for the agent"}),{target:{value:"Doplň shrnutí"}})}
const consent=()=>fireEvent.click(screen.getByRole("checkbox",{name:"The agent may edit the whole document"}))
const sends=()=>transport.send.mock.calls.map(([frame])=>frame as {type:string;payload:string}).filter(frame=>frame.type==="send_message")
async function sent(){
 await waitFor(()=>expect(sends()).toHaveLength(1))
 return JSON.parse(sends()[0].payload) as {session_id:string;content:string;metadata?:unknown}
}
const event=(seq:number,payload:unknown)=>({type:"chat_event",channel:"session:chat-1",seq,payload})
const echo=(content:string,id="message-1",seq=41)=>act(()=>{transport.onMessage?.({type:"run_begin",channel:"session:chat-1",seq:seq-1,payload:{from_seq:seq-1}});transport.onMessage?.(event(seq,{type:"user_message",content,metadata:{author_user_id:"human",message_id:id}}))})
const done=(seq=42)=>act(()=>{transport.onMessage?.(event(seq,{type:"done",content:"",metadata:{no_reply:true}}))})

beforeEach(()=>{
 availability=()=>json({enabled:true,can_edit:true,preview_enabled:true,agents_enabled:true})
 profile=()=>json({mode:"trusted"})
 task=()=>json({work_id:"work-1",status:"queued"},202)
 m.fetch.mockImplementation(async(url:string)=>{
  if(url.includes("/craft/availability"))return availability()
  if(url.includes("/execution-profile"))return profile()
  if(url.includes("/craft/tasks"))return task()
  if(url.includes("/chats"))return json({id:"chat-1"},201)
  return json({token:"t"})
 })
})
afterEach(()=>{cleanup();vi.useRealTimers();transport.connected=true;transport.send.mockReset();m.fetch.mockReset();props.onStarted.mockReset()})

describe("native agent start control",()=>{
 it.each([
  {name:"agents flag off",value:{enabled:true,can_edit:true,agents_enabled:false}},
  {name:"non-literal flag",value:{enabled:true,can_edit:true,agents_enabled:"true"}},
  {name:"no write access",value:{enabled:true,can_edit:false,agents_enabled:true}},
 ])("stays absent with $name",async({value})=>{
  availability=()=>json(value);render(<NativeCraftAgentStart {...props}/>)
  await waitFor(()=>expect(calls("/craft/availability")).toHaveLength(1))
  await act(async()=>{await Promise.resolve()})
  expect(screen.queryByRole("button",{name:"Edit with an agent"})).toBeNull()
 })
 it("stays absent when availability cannot be confirmed",async()=>{
  availability=()=>{throw Error("offline")};render(<NativeCraftAgentStart {...props}/>)
  await waitFor(()=>expect(calls("/craft/availability")).toHaveLength(1))
  await act(async()=>{await Promise.resolve()})
  expect(screen.queryByRole("button",{name:"Edit with an agent"})).toBeNull()
 })
 it("asks nothing and shows nothing while the host does not allow it",async()=>{
  render(<NativeCraftAgentStart {...props} enabled={false}/>)
  await act(async()=>{await Promise.resolve()})
  expect(m.fetch).not.toHaveBeenCalled();expect(screen.queryByRole("button",{name:"Edit with an agent"})).toBeNull()
 })
 it("placed in a drawer, the form is the content: no Edit with an agent toggle and no Cancel",async()=>{
  render(<NativeCraftAgentStart {...props} place={form=><div data-testid="drawer">{form}</div>}/>)
  const drawer=await screen.findByTestId("drawer")
  expect(await screen.findByRole("group",{name:"Edit with an agent"})).toBeInTheDocument()
  expect(drawer).toContainElement(screen.getByRole("combobox",{name:"Agent"}))
  expect(screen.queryByRole("button",{name:"Edit with an agent"})).toBeNull()
  expect(screen.queryByRole("button",{name:"Cancel"})).toBeNull()
  expect(start()).toBeDisabled()
 })
 it("needs an explicit whole-document consent before anything is created",async()=>{
  render(<NativeCraftAgentStart {...props}/>);await openForm()
  const box=screen.getByRole("checkbox",{name:"The agent may edit the whole document"})
  expect(box).not.toBeChecked();expect(start()).toBeDisabled()
  fireEvent.click(start());expect(calls("/chats")).toHaveLength(0)
  consent();expect(start()).toBeEnabled()
 })
 it("records the instruction without a chat turn and binds the task to the persisted message once the send settled",async()=>{
  render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  const payload=await sent()
  expect(calls("/api/v1/agents/agent-1/chats?workspace_id=w")[0][1]).toMatchObject({method:"POST",body:JSON.stringify({origin:"UI"})})
  expect(payload).toEqual({session_id:"chat-1",content:"Doplň shrnutí",metadata:{native_craft_instruction:true}})
  echo(payload.content)
  expect(taskCalls()).toHaveLength(0)
  done()
  await waitFor(()=>expect(taskCalls()).toHaveLength(1))
  const [url,init]=taskCalls()[0] as [string,RequestInit]
  expect(url).toBe("/api/v1/pages/%40p/craft/tasks?workspace_id=w")
  expect(init).toMatchObject({method:"POST",cache:"no-store",redirect:"error",skipRefresh:true})
  expect(JSON.parse(String(init.body))).toEqual({chat_id:"chat-1",message_id:"message-1",whole_document:true})
  await waitFor(()=>expect(props.onStarted).toHaveBeenCalledWith("work-1"))
  // Consent is per start: the next task needs a new message and a new action.
  fireEvent.click(await screen.findByRole("button",{name:"Edit with an agent"}))
  expect(screen.getByRole("checkbox",{name:"The agent may edit the whole document"})).not.toBeChecked()
 })
 it("never starts a task without the persisted echo of this exact message",async()=>{
  render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  await sent();echo("a different message");done()
  expect(await screen.findByRole("alert")).toHaveTextContent("No task was created")
  expect(taskCalls()).toHaveLength(0)
 })
 it("reports a refused send without starting a task",async()=>{
  render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start());await sent()
  act(()=>{transport.onMessage?.({type:"error",channel:"session:chat-1",payload:{error:"a message is already being processed"}})})
  expect(await screen.findByRole("alert")).toHaveTextContent("No task was created");expect(taskCalls()).toHaveLength(0)
  expect(screen.getByRole("checkbox",{name:"The agent may edit the whole document"})).not.toBeChecked()
 })
 it("sends nothing on a chat whose execution profile is not trusted",async()=>{
  profile=()=>json({mode:"restricted"});render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  expect(await screen.findByRole("alert")).toHaveTextContent("No task was created")
  expect(sends()).toHaveLength(0);expect(taskCalls()).toHaveLength(0)
 })
 it("abandons the start when the host stops allowing it before the send settled",async()=>{
  const view=render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  const payload=await sent();echo(payload.content)
  view.rerender(<NativeCraftAgentStart {...props} enabled={false}/>)
  expect(screen.queryByRole("button",{name:"Start agent"})).toBeNull()
  done();await act(async()=>{await Promise.resolve()})
  expect(taskCalls()).toHaveLength(0)
  view.rerender(<NativeCraftAgentStart {...props}/>)
  expect(await screen.findByRole("alert")).toHaveTextContent("No task was created")
 })
 it("posts nothing when the echo, the end of the chat and the retirement commit in the same render",async()=>{
  const view=render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  const payload=await sent()
  await act(async()=>{
   transport.onMessage?.({type:"run_begin",channel:"session:chat-1",seq:40,payload:{from_seq:40}})
   transport.onMessage?.(event(41,{type:"user_message",content:payload.content,metadata:{author_user_id:"human",message_id:"message-1"}}))
   transport.onMessage?.(event(42,{type:"done",content:"",metadata:{no_reply:true}}))
   view.rerender(<NativeCraftAgentStart {...props} enabled={false}/>)
  })
  await act(async()=>{await Promise.resolve()})
  expect(taskCalls()).toHaveLength(0);expect(props.onStarted).not.toHaveBeenCalled()
 })
 it("sends nothing when the connection opens in the render that retires the host",async()=>{
  transport.connected=false
  const view=render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  await screen.findByRole("status");transport.connected=true
  await act(async()=>{view.rerender(<NativeCraftAgentStart {...props} enabled={false}/>)})
  expect(sends()).toHaveLength(0)
 })
 it("does not resend a refused or unconfirmed task",async()=>{
  task=()=>json({error:"denied"},403);render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  const payload=await sent();echo(payload.content);done()
  expect(await screen.findByRole("alert")).toHaveTextContent("may not")
  await act(async()=>{await Promise.resolve()})
  expect(taskCalls()).toHaveLength(1);expect(props.onStarted).not.toHaveBeenCalled()
  expect(screen.getByRole("checkbox",{name:"The agent may edit the whole document"})).not.toBeChecked()
 })
 it("does not claim a start without the accepted reply",async()=>{
  task=()=>json({work_id:"",status:"queued"},202);render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  const payload=await sent();echo(payload.content);done()
  expect(await screen.findByRole("alert")).toHaveTextContent("was not confirmed");expect(props.onStarted).not.toHaveBeenCalled()
 })
 it("gives up when the send never settles",async()=>{
  vi.useFakeTimers({shouldAdvanceTime:true})
  render(<NativeCraftAgentStart {...props}/>);await openForm();consent();fireEvent.click(start())
  const payload=await sent();echo(payload.content)
  await act(async()=>{await vi.advanceTimersByTimeAsync(30_000)})
  expect(screen.getByRole("alert")).toHaveTextContent("No task was created");expect(taskCalls()).toHaveLength(0)
 })
 it("explains the proposal route on a document a person holds and starts the same way",async()=>{
  render(<NativeCraftAgentStart {...props} holder="human"/>);await openForm()
  expect(screen.getByText(/prepares a proposal/)).toBeInTheDocument()
  consent();fireEvent.click(start())
  const payload=await sent();echo(payload.content);done()
  await waitFor(()=>expect(taskCalls()).toHaveLength(1))
  expect(JSON.parse(String((taskCalls()[0][1] as RequestInit).body))).toEqual({chat_id:"chat-1",message_id:"message-1",whole_document:true})
 })
 it("does not offer a start while an agent holds the document",async()=>{
  render(<NativeCraftAgentStart {...props} holder="agent"/>);await openForm();consent()
  expect(start()).toBeDisabled();expect(screen.getByText(/An agent is editing/)).toBeInTheDocument()
 })
})
