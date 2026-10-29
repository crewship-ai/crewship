"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient, type MutateOptions } from "@tanstack/react-query"
import type { ArtifactOrigin } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch, tryRefresh } from "@/lib/api-fetch"
import { getBearerToken } from "@/lib/server-base"
import { assertWorkspaceRequest, captureWorkspaceRequest } from "@/lib/workspace-availability"

export interface ArtifactFile { path: string; content: string; media_type: string }
export type ArtifactAuthorKind = "human" | "agent"
export interface ArtifactComment { id: string; version: number; path: string; selector: string; quote: string; text: string; author_kind?: ArtifactAuthorKind; task_id?: string; replies?: { text: string; version: number; author_kind?: ArtifactAuthorKind }[] }
export interface ArtifactProjectSnapshot {
 page_id: string
 artifact_kind?: "slides"
 origin?: ArtifactOrigin
 can_write: boolean
 can_share?: boolean
 can_restore?: boolean
 can_send_to_agent?: boolean
 can_comment?: boolean
 latest_version: number
 shared_version: number
 manifest: { version: number; entrypoint: string; files: ArtifactFile[] }
 versions: { version: number; created_at?: string; message?: string }[]
 quota: { used_bytes: number; limit_bytes: number; warning: boolean }
 comments: ArtifactComment[]
}
export interface ArtifactAnchor { version: number; path: string; selector: string; quote: string }
let projectScopeSequence=0
export interface ArtifactProjectSave { base_version: number; entrypoint: string; files: ArtifactFile[] }
export function useArtifactProject(workspace: string, slug: string, version?: number) {
 const client = useQueryClient()
 const session = useSessionSafe()
 const human = session?.data?.user?.id
 const credential = getBearerToken()
 const scope = useMemo(() => ({binding:[workspace,slug,human,version],id:++projectScopeSequence,active:true, controllers:new Set<AbortController>(),credential,pageId:undefined as string|undefined,refreshedAt:undefined as number|undefined}), [workspace,slug,human,version,credential])
 const current = useRef(scope)
 if(current.current !== scope) {
  current.current.active=false
  for(const controller of current.current.controllers)controller.abort()
  current.current=scope
 }
 useEffect(() => {
  scope.active=true
  return () => {scope.active=false;for(const controller of scope.controllers)controller.abort()}
 },[scope])
 const [refusal,setRefusal]=useState<{scope:typeof scope;error:Error}>()
 const key = ["artifact-project", workspace, slug, human ?? "anonymous"]
 const endpoint = `/api/v1/pages/${encodeURIComponent(slug)}/artifact`
 const params = new URLSearchParams({ workspace_id: workspace })
 const valid = () => scope.active && current.current === scope && scope.credential === getBearerToken() && Boolean(human && workspace && slug)
 const request = async <T,>(path: string, body?: unknown, signal?:AbortSignal): Promise<T> => {
  if(!valid()||signal?.aborted)throw new Error("Project request retired.")
  const controller=new AbortController()
  const abort=()=>controller.abort()
  signal?.addEventListener("abort",abort,{once:true});scope.controllers.add(controller)
  try {
   const assertActive=()=>{if(!valid()||controller.signal.aborted)throw new Error("Project request retired.")}
   assertActive()
   const writing=body!==undefined
   // A draft stays bound to the page we read, even if its slug is reassigned.
   if(writing&&!scope.pageId)throw new Error("Load the project before saving.")
   const target=writing?`/api/v1/pages/${encodeURIComponent("@"+scope.pageId)}/artifact`:endpoint
   const url=`${target}${path}${path.includes("?") ? "&" : "?"}${params}`
   const captured=captureWorkspaceRequest(url,writing?{method:"POST"}:undefined)
   if(writing&&scope.credential===null){
    // Refresh BEFORE dispatch: a failed or ambiguous artifact write is never
    // replayed. Polling cannot guarantee a fresh cookie at the next save.
    const now=Date.now()
    if(scope.refreshedAt===undefined||now<scope.refreshedAt||now-scope.refreshedAt>=5*60_000){
     const refreshed=await tryRefresh()
     assertActive();assertWorkspaceRequest(captured)
     if(refreshed!=="ok")throw Object.assign(new Error("Could not confirm fresh authentication. Your draft is preserved."),{status:refreshed==="auth_failed"?401:503})
     scope.refreshedAt=Date.now()
    }
    // Refresh cookies may belong to another identity. Never trust the cached
    // React session, an empty response, or a failed identity read for a write.
    const identity=await apiFetch("/api/auth/session",{signal:controller.signal,cache:"no-store",redirect:"error",skipRefresh:true})
    assertActive();assertWorkspaceRequest(captured)
    const authenticated=await identity.json().catch(()=>null)
    assertActive();assertWorkspaceRequest(captured)
    if(!identity.ok||authenticated?.user?.id!==human)throw Object.assign(new Error("Could not confirm the original project editor. Your draft is preserved."),{status:identity.ok?403:identity.status})
   }
   assertActive();assertWorkspaceRequest(captured)
   const response = await apiFetch(url, {signal:controller.signal,cache:"no-store",redirect:"error",skipRefresh:writing,...(!writing ? {} : {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)})})
   const data = await response.json().catch(() => null)
   if(!valid()||controller.signal.aborted)throw new Error("Project request retired.")
   if (!response.ok) {
    const error=Object.assign(new Error(response.status === 409 ? "This project changed. Your draft is preserved. Load the latest version before saving again." : data?.error ?? "Could not load the artifact project."), { status: response.status })
    throw error
   }
   if(!writing&&typeof data?.page_id==="string"){
    if(scope.pageId&&scope.pageId!==data.page_id)throw new Error("Project identity changed. Reload before saving.")
    scope.pageId=data.page_id
   }
   return data
  } catch(error) {
   const status=(error as Error&{status?:number}).status
   if(valid()&&(status===401||status===403))setRefusal({scope,error:error as Error})
   throw error
  } finally {scope.controllers.delete(controller);signal?.removeEventListener("abort",abort)}
 }
 const query = useQuery({ queryKey: [...key, version ?? "latest",scope.id], enabled: Boolean(human && workspace && slug), retry: false, gcTime: 0, refetchInterval: 15_000, queryFn: ({signal}) => request<ArtifactProjectSnapshot>(version ? `?version=${version}` : "",undefined,signal) })
 const invalidate = () => { if(valid())void client.invalidateQueries({ queryKey: key }) }
 const restore = useScopedProjectMutation<{version:number;base_version:number}>(scope,valid,body=>request("/restore",body),invalidate)
 const share = useScopedProjectMutation<{version:number;base_version:number}>(scope,valid,body=>request("/share",body),invalidate)
 const save = useScopedProjectMutation<ArtifactProjectSave,{version:number}>(scope,valid,body=>request("/versions",body),invalidate)
 const comment = useScopedProjectMutation<ArtifactAnchor & {text:string;send_to_agent:boolean},ArtifactComment>(scope,valid,body=>request("/comments",body),invalidate)
 return { query:refusal?.scope===scope?{...query,isError:true,error:refusal.error}:query,restore,share,save,comment }
}

/** Capture scope in variables before React Query schedules mutationFn. Observer
 * options can change while a queued mutation has not begun yet. */
function useScopedProjectMutation<TBody,TResult=unknown>(scope:object,valid:()=>boolean,request:(body:TBody)=>Promise<TResult>,onSuccess:()=>void){
 type Input={scope:object;body:TBody}
 type Callbacks=MutateOptions<TResult,Error,TBody,unknown>
 const mutation=useMutation<TResult,Error,Input,unknown>({retry:false,mutationFn:input=>{
  if(input.scope!==scope||!valid())throw new Error("Project request retired.")
  return request(input.body)
 },onSuccess})
 const callbacks=(options?:Callbacks):MutateOptions<TResult,Error,Input,unknown>=>({
  onSuccess:(data,input,result,context)=>{if(valid()&&input.scope===scope)options?.onSuccess?.(data,input.body,result,context)},
  onError:(error,input,result,context)=>{if(valid()&&input.scope===scope)options?.onError?.(error,input.body,result,context)},
  onSettled:(data,error,input,result,context)=>{if(valid()&&input.scope===scope)options?.onSettled?.(data,error,input.body,result,context)},
 })
 return {...mutation,
  mutate:(body:TBody,options?:Callbacks)=>{if(valid())mutation.mutate({scope,body},callbacks(options))},
  mutateAsync:(body:TBody,options?:Callbacks)=>valid()?mutation.mutateAsync({scope,body},callbacks(options)):Promise.reject(new Error("Project request retired.")),
 }
}
