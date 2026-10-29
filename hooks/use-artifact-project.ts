"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient, type MutateOptions } from "@tanstack/react-query"
import type { ArtifactOrigin } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"

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
 const scope = useMemo(() => ({binding:[workspace,slug,human,version],id:++projectScopeSequence,active:true, controllers:new Set<AbortController>()}), [workspace,slug,human,version])
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
 const valid = () => scope.active && current.current === scope && Boolean(human && workspace && slug)
 const request = async <T,>(path: string, body?: unknown, signal?:AbortSignal): Promise<T> => {
  if(!valid()||signal?.aborted)throw new Error("Project request retired.")
  const controller=new AbortController()
  const abort=()=>controller.abort()
  signal?.addEventListener("abort",abort,{once:true});scope.controllers.add(controller)
  try {
   if(!valid()||signal?.aborted)throw new Error("Project request retired.")
   const response = await apiFetch(`${endpoint}${path}${path.includes("?") ? "&" : "?"}${params}`, {signal:controller.signal,cache:"no-store",redirect:"error",skipRefresh:body !== undefined,...(body === undefined ? {} : {method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)})})
   const data = await response.json().catch(() => null)
   if(!valid()||controller.signal.aborted)throw new Error("Project request retired.")
   if (!response.ok) {
    const error=Object.assign(new Error(response.status === 409 ? "This project changed. Your draft is preserved. Load the latest version before saving again." : data?.error ?? "Could not load the artifact project."), { status: response.status })
    if(response.status===401||response.status===403)setRefusal({scope,error})
    throw error
   }
   return data
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
