import {afterEach,describe,expect,it,vi} from "vitest"
import {cleanup,renderHook,waitFor} from "@testing-library/react"
import {QueryClient,QueryClientProvider} from "@tanstack/react-query"
import type {ReactNode} from "react"
import {useCrewManagement} from "../use-crew-management"
const h=vi.hoisted(()=>({fetch:vi.fn(),user:"actor",role:"MEMBER",mode:"trusted",status:"authenticated"}))
vi.mock("@/lib/api-fetch",()=>({apiFetch:h.fetch}))
vi.mock("@/hooks/use-auth",()=>({useAuth:()=>({session:{user:{id:h.user}},status:h.status})}))
vi.mock("@/hooks/use-abilities",()=>({useAbilities:()=>({role:h.role,loading:false})}))
vi.mock("@/hooks/use-access-mode",()=>({useAccessMode:()=>h.mode}))
const body={mode:"trusted",account_restricted:false,decisions:[{kind:"crew",id:"crew",operation:"manage",state:"allowed"}]}
const response=(data:unknown,status=200)=>({ok:status===200,status,json:async()=>data})
function wrapper(){const client=new QueryClient();return ({children}:{children:ReactNode})=><QueryClientProvider client={client}>{children}</QueryClientProvider>}
afterEach(()=>{cleanup();h.fetch.mockReset();h.user="actor";h.role="MEMBER";h.mode="trusted";h.status="authenticated"})
describe("live trusted crew management hint",()=>{
 it.each(["MEMBER","MANAGER"])("requires an exact allowed own grant for %s",async role=>{
  h.role=role;h.fetch.mockResolvedValue(response(body));const {result}=renderHook(()=>useCrewManagement("ws","crew"),{wrapper:wrapper()});
  expect(result.current.canManage).toBe(false);await waitFor(()=>expect(result.current.canManage).toBe(true));
  expect(h.fetch.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/access/me");
 });
 it.each(["VIEWER","ADMIN","OWNER"])("never turns role %s into an additive grant",role=>{
  h.role=role;const {result}=renderHook(()=>useCrewManagement("ws","crew"),{wrapper:wrapper()});expect(result.current.canManage).toBe(false);expect(h.fetch).not.toHaveBeenCalled();
 });
 it.each([{...body,account_restricted:true},{...body,mode:"restricted"},{...body,decisions:[{...body.decisions[0],state:"denied"}]},{...body,decisions:[{...body.decisions[0],id:"other"}]},{}])("fails closed for ceilings, inactive/other grants and malformed data",async data=>{
  h.fetch.mockResolvedValue(response(data));const {result}=renderHook(()=>useCrewManagement("ws","crew"),{wrapper:wrapper()});await waitFor(()=>expect(result.current.loading).toBe(false));expect(result.current.canManage).toBe(false);
 });
 it("withdraws the hint on a failed refresh",async()=>{
  h.fetch.mockResolvedValueOnce(response(body)).mockResolvedValueOnce(response({},403));const {result}=renderHook(()=>useCrewManagement("ws","crew"),{wrapper:wrapper()});await waitFor(()=>expect(result.current.canManage).toBe(true));
  void result.current.refresh();await waitFor(()=>expect(result.current.error).toBe(true));expect(result.current.canManage).toBe(false);
 });
 it.each(["workspace","crew","actor"])("does not reuse an old allow across %s switches",async field=>{
  h.fetch.mockResolvedValueOnce(response(body)).mockImplementation(()=>new Promise(()=>{}));let ws="ws",crew="crew";
  const {result,rerender}=renderHook(()=>useCrewManagement(ws,crew),{wrapper:wrapper()});await waitFor(()=>expect(result.current.canManage).toBe(true));
  if(field==="workspace")ws="other";else if(field==="crew")crew="other";else h.user="other";
  rerender();expect(result.current.canManage).toBe(false);
 });
 it("does not request or retain a hint for a Guest or signed-out actor",()=>{
  h.mode="restricted";const {result,rerender}=renderHook(()=>useCrewManagement("ws","crew"),{wrapper:wrapper()});expect(result.current.canManage).toBe(false);h.mode="trusted";h.status="unauthenticated";rerender();expect(result.current.canManage).toBe(false);expect(h.fetch).not.toHaveBeenCalled();
 });
})
