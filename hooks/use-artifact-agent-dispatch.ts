"use client"

import { useCallback, useEffect, useRef } from "react"
import { useQuery } from "@tanstack/react-query"
import { useChat } from "@/hooks/use-chat"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { resolveWsBase } from "@/lib/server-base"
import type { ArtifactComment } from "@/hooks/use-artifact-project"

export interface ArtifactOrigin { chat_id: string; agent_id: string; agent_slug?: string }
export function useArtifactAgentDispatch(workspaceId: string, pageId: string | undefined, origin: ArtifactOrigin | undefined, enabled: boolean, kind: "project" | "design" = "project") {
 const user = useSessionSafe()?.data?.user?.id
 const scope = JSON.stringify([user,workspaceId,pageId,origin?.chat_id])
 const pending = useRef(new Map<string,{resolve:(ok:boolean)=>void;timer:ReturnType<typeof setTimeout>}>())
 const profile = useQuery({queryKey:["artifact-origin-profile",workspaceId,origin?.chat_id,user],enabled:Boolean(enabled&&user&&origin),retry:false,gcTime:0,queryFn:async({signal})=>{
  const response=await apiFetch(`/api/v1/chats/${encodeURIComponent(origin!.chat_id)}/execution-profile?workspace_id=${encodeURIComponent(workspaceId)}`,{signal,cache:"no-store"})
  if(!response.ok)return false
  return (await response.json())?.mode==="trusted"
 }})
 useEffect(()=>{const entries=pending.current;return ()=>{for(const entry of entries.values()){clearTimeout(entry.timer);entry.resolve(false)}entries.clear()}},[scope])
 const getToken = useCallback(async()=>{
  const response=await apiFetch("/api/v1/ws-token")
  if(!response.ok)return null
  const body=await response.json();return typeof body?.token==="string"?body.token:null
 },[])
 const saved=useCallback((_text:string,metadata?:Record<string,unknown>)=>{
  const intent=metadata?.artifact_project_intent as {submission_id?:string}|undefined
  const entry=intent?.submission_id?pending.current.get(intent.submission_id):undefined
  if(entry){clearTimeout(entry.timer);entry.resolve(true);pending.current.delete(intent!.submission_id!)}
 },[])
 // The server refuses a send while the agent is already running. That frame
 // carries no submission id; this hook has at most one send in flight, so
 // it settles the pending confirmation instead of waiting for the timeout.
 const refusals=useRef(new Map<string,string>())
 const refused=useCallback((reason:string)=>{
  for(const [id,entry] of pending.current){clearTimeout(entry.timer);refusals.current.set(id,reason);entry.resolve(false)}
  pending.current.clear()
 },[])
 const base=resolveWsBase()
 const chat=useChat({workspaceId,sessionId:origin?.chat_id??"",currentUserId:user,wsUrl:base?`${base}/ws`:"",getToken,realtimeEnabled:Boolean(enabled&&origin&&profile.data&&!profile.isError),canSend:Boolean(enabled&&origin&&profile.data&&!profile.isError),executionProfile:profile.data?"trusted":"pending",onOwnMessageSaved:saved,onSendRefused:refused})
 // This hook renders no transcript, so it never loads history. Open the
 // sequenced-event gate anyway: the server's echo of our message and the
 // run's done are sequenced and would otherwise never be applied.
 const realtime=Boolean(enabled&&origin&&profile.data&&!profile.isError)
 const {markHistoryUnavailable}=chat
 useEffect(()=>{if(realtime)markHistoryUnavailable()},[realtime,markHistoryUnavailable,scope])
 const dispatch=async(comment:ArtifactComment):Promise<boolean>=>{
  if(!enabled||!profile.data||profile.isError||!pageId||!origin||chat.connectionStatus!=="connected"||chat.isStreaming)return false
  // A retry refers to the same queued task; the server owns replay admission.
  const submissionId=`artifact-comment-${comment.id}`
  if(pending.current.has(submissionId))return false
  refusals.current.delete(submissionId)
  const confirmation=new Promise<boolean>(resolve=>{const timer=setTimeout(()=>{pending.current.delete(submissionId);resolve(false)},15000);pending.current.set(submissionId,{resolve,timer})})
  const prompt = kind === "design" ? `Update Design @${pageId} according to comment ${comment.id}: ${comment.text}. Selected node: ${comment.selector}, original version: ${comment.version}. Use design_get, then design_apply; a successful design_apply already saves the new version and answers the comment, so do not call design_snapshot afterwards. Preserve concurrent changes and report the confirmed version.` : `Update the artifact project according to this exact comment. Page: @${pageId}. Base version: ${comment.version}. File: ${comment.path}. Element: ${comment.selector}. Selected text: ${comment.quote}\nRequested change: ${comment.text}\nPublish a new version with base_version ${comment.version}, then reply to comment ${comment.id} with the new version and what changed. If the anchor no longer matches, ask for clarification.`
  const sent=await chat.sendMessage(prompt,{artifact_project_intent:{...(kind === "design" ? {kind:"design"} : {}),submission_id:submissionId,page_id:pageId,comment_id:comment.id,base_version:comment.version}})
  if(sent===false){const entry=pending.current.get(submissionId);if(entry){clearTimeout(entry.timer);entry.resolve(false);pending.current.delete(submissionId)}}
  return confirmation
 }
 /** Why the last delivery of this comment was refused, if the server said so. */
 const refusal=(commentId:string)=>refusals.current.get(`artifact-comment-${commentId}`)
 return {dispatch,refusal,ready:chat.connectionStatus==="connected"&&!chat.isStreaming&&Boolean(enabled&&profile.data&&!profile.isError)}
}
