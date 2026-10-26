import {afterEach,describe,expect,it,vi} from "vitest"
import {act,cleanup,render,screen} from "@testing-library/react"
import {QueryClient,QueryClientProvider} from "@tanstack/react-query"
import {CrewSettings} from "../crew-settings"
const h=vi.hoisted(()=>({fetch:vi.fn(),actor:"actor",change:(_members:unknown[])=>{}}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:h.fetch}))
vi.mock("@/hooks/use-abilities",()=>({useAbilities:()=>({role:"MEMBER"})}))
vi.mock("@/hooks/use-crew-management",()=>({useCrewManagement:()=>({trusted:true,actorID:h.actor,canManage:true,loading:false,error:false})}))
vi.mock("../crew-canvas-tabs/settings-tab",()=>({SettingsTab:({trailing}:{trailing:React.ReactNode})=><div>{trailing}</div>}))
vi.mock("../crew-members",()=>({CrewMembers:({members,onMembersChange}:{members:{name:string}[];onMembersChange:(rows:unknown[])=>void})=>{h.change=onMembersChange;return <div>{members.map(m=><span key={m.name}>{m.name}</span>)}</div>}}))
vi.mock("../crew-budget",()=>({CrewBudgetCard:()=>null}))
vi.mock("../crew-yaml",()=>({CrewCommandLine:()=>null}))
afterEach(()=>{cleanup();h.fetch.mockReset();h.actor="actor"})
describe("crew roster management context",()=>{
 it.each(["crew","workspace","actor"])("withdraws old rows across %s switches and ignores late old callbacks",async field=>{
  let finish:(res:unknown)=>void=()=>{};
  h.fetch.mockResolvedValueOnce({ok:true,json:async()=>[{name:"Old roster"}]}).mockImplementation(()=>new Promise(resolve=>{finish=resolve}));
  const client=new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}});
  let workspace="ws",crew="first";
  const content=()=> <QueryClientProvider client={client}><CrewSettings workspaceId={workspace} crew={{id:crew,name:crew,slug:crew} as never} agents={[]} patch={vi.fn()} onApplyAvatarStyle={vi.fn()}/></QueryClientProvider>;
  const {rerender}=render(content());await screen.findByText("Old roster");const oldCallback=h.change;
  if(field==="crew")crew="next";else if(field==="workspace")workspace="next";else h.actor="next";
  rerender(content());expect(screen.queryByText("Old roster")).toBeNull();
  await act(async()=>finish({ok:true,json:async()=>[{name:"New roster"}]}));await screen.findByText("New roster");
  await act(async()=>oldCallback([{name:"Late old mutation"}]));expect(screen.getByText("New roster")).toBeTruthy();expect(screen.queryByText("Late old mutation")).toBeNull();
 });
});
