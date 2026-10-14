"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { AuthSplitShell } from "@/components/branding/auth-split-shell"
import { Button } from "@/components/ui/button"
import { refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { serverFetch } from "@/lib/server-base"
import { captureInvitationProof, clearInvitationProof } from "@/lib/invitation-proof"

export default function JoinPage() {
 const router=useRouter()
 const [token,setToken]=useState<string|null>(null)
 const [guestConfirmation,setGuestConfirmation]=useState(false)
 const [status,setStatus]=useState<"idle"|"saving"|"signin"|"error">("idle")
 useEffect(()=>{setToken(captureInvitationProof())},[])
 const path="/join"
 async function accept() {
  if(!token || status==="saving") return
  setStatus("saving")
  try {
   const response=await serverFetch("/api/v1/auth/invitation/accept",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({token,...(guestConfirmation?{confirm_account_restriction:true}:{})})})
   if(response.status===401){setStatus("signin");return}
   if(!response.ok){
    const body=await response.json().catch(()=>null)
    if(body?.code==="guest_confirmation_required"){setGuestConfirmation(true);setStatus("idle");return}
    setStatus("error");return
   }
   clearInvitationProof()
   await refreshWorkspaceSettings()
   router.replace("/")
  } catch {setStatus("error")}
 }
 return <AuthSplitShell eyebrow="Workspace invitation" headline="Join your workspace." blurb="Accept the invitation using the account it was sent to.">
  <div className="w-full max-w-sm space-y-5">
   <h1 className="text-xl font-semibold">Accept invitation</h1>
   <p className="text-sm text-muted-foreground">Sign in with the invited email address, then accept this invitation.</p>
   {token==="" && <p role="alert">Invitation link is missing its token. Ask for the original invitation link.</p>}
   {status==="error" && <p role="alert">This invitation is unavailable. Check the account or ask for a new invitation.</p>}
   {status==="signin" && <p role="status">Sign in to accept this invitation.</p>}
   {guestConfirmation && <p role="status" className="text-sm">This invitation makes you a Guest. Accepting it restricts your entire account across all workspaces to explicitly shared resources. Your other workspaces will also follow the Guest restriction.</p>}
   <Button disabled={!token || status==="saving"} onClick={accept}>{status==="saving" ? "Accepting…" : guestConfirmation ? "Accept Guest restrictions" : "Accept invitation"}</Button>
   {token && <div className="space-y-2 text-sm">
    <a className="block text-primary underline" href={`/login?redirect=${encodeURIComponent(path)}`}>Sign in</a>
    <a className="block text-primary underline" href="/signup">Create an account</a>
   </div>}
  </div>
 </AuthSplitShell>
}
