"use client"

import {useQuery} from "@tanstack/react-query"
import {z} from "zod"
import {useAuth} from "@/hooks/use-auth"
import {useAbilities} from "@/hooks/use-abilities"
import {useAccessMode} from "@/hooks/use-access-mode"
import {apiFetch} from "@/lib/api-fetch"
import {requireApiPathSegment} from "@/lib/api-path-segment"

const ownSchema=z.object({
  mode:z.enum(["trusted","restricted"]), account_restricted:z.boolean(),
  decisions:z.array(z.object({kind:z.string(),id:z.string(),operation:z.string(),state:z.string()})),
})

/** An affordance hint only: every mutation rechecks authority on the server. */
export function useCrewManagement(workspaceId:string,crewId:string){
 const {session,status}=useAuth()
 const {role,loading}=useAbilities()
 const mode=useAccessMode()
 const trusted=status === "authenticated" && !loading && mode === "trusted"
 const enabled=trusted && !!session?.user.id && !!workspaceId && !!crewId && (role === "MEMBER" || role === "MANAGER")
 const query=useQuery({
  queryKey:["crew-management-access",workspaceId,crewId,session?.user.id,role,mode,status],
  enabled,retry:false,staleTime:0,gcTime:0,refetchInterval:30_000,refetchIntervalInBackground:false,
  queryFn:async({signal})=>{
   const response=await apiFetch(`/api/v1/workspaces/${requireApiPathSegment(workspaceId)}/access/me`,{signal})
   if(!response.ok)throw new Error("Crew management access unavailable")
   return ownSchema.parse(await response.json())
  },
 })
 const canManage=enabled && query.isSuccess && !query.isFetching && !query.isError && query.data.mode === "trusted" && !query.data.account_restricted && query.data.decisions.some(d=>d.kind === "crew" && d.id === crewId && d.operation === "manage" && d.state === "allowed")
 return {trusted,actorID:session?.user.id,canManage,loading:enabled && query.isFetching,error:enabled && query.isError,refresh:query.refetch}
}
