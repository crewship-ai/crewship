"use client"

import { useEffect, useRef, useState } from "react"
import { FileCode2 } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import {
  ArtifactPolicyError, policyAccessDenied, validArtifactPolicy,
  useArtifactCreationPolicy, type ArtifactCreationPolicy, type ArtifactPolicyCrew,
} from "@/hooks/use-artifact-creation-policy"
import { SettingsCard, SettingsRow } from "../shared"
import { Switch } from "@/components/ui/switch"
import { Checkbox } from "@/components/ui/checkbox"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SaveFooter } from "@/components/ui/save-footer"
import {
  AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle,
  AlertDialogDescription, AlertDialogFooter, AlertDialogAction, AlertDialogCancel,
} from "@/components/ui/alert-dialog"

export function ArtifactCreationPolicyCard({ workspaceId, role }: { workspaceId: string; role: string | null }) {
  const policy = useArtifactCreationPolicy(workspaceId, role)
  if (!policy.canManage) return null
  return <SettingsCard title="Agent artifact creation" icon={FileCode2} description="Choose which crews may create and publish artifacts. Human run opt-in and Page permissions are still required.">
    {policy.error && <p role="alert" className="p-4 text-sm text-destructive">
      {policy.error.message}
      {!policy.data && <Button variant="outline" className="ml-2 coarse:h-12" onClick={policy.reviewLatest}>Retry</Button>}
    </p>}
    {policy.data ? <PolicyForm
      key={policy.identity}
      workspace={workspaceId}
      initial={policy.data.policy}
      crews={policy.data.crews}
      onReload={policy.reviewLatest}
      onSaved={policy.saved}
      onDenied={policy.deny}
    /> : !policy.error && <Skeleton className="m-4 h-20" />}
  </SettingsCard>
}
interface PolicyFormProps {
  workspace: string
  initial: ArtifactCreationPolicy
  crews: ArtifactPolicyCrew[]
  onReload: () => void
  onSaved: (policy: ArtifactCreationPolicy) => void
  onDenied: (error: ArtifactPolicyError) => void
}
function PolicyForm({ workspace, initial, crews, onReload, onSaved, onDenied }: PolicyFormProps) {
  // Keep fields and CAS authority together. A background read never advances
  // this baseline; only a confirmed save or explicit review replaces it.
  const [baseline, setBaseline] = useState(initial)
  const form = useDirtyForm({ enabled: baseline.enabled, crewIds: JSON.stringify(baseline.crew_ids) })
  const [uncertain, setUncertain] = useState(false)
  const [confirmReload, setConfirmReload] = useState(false)
  const active = useRef<{ controller: AbortController | null }>({ controller: null })
  useEffect(() => {
    const request = active.current
    return () => request.controller?.abort()
  }, [])
  const ids = JSON.parse(form.draft.crewIds) as string[]
  const locked = form.status === "saving" || uncertain
  const options = [...crews, ...ids.filter(id => !crews.some(crew => crew.id === id)).map(id => ({ id, name: `Unavailable crew (${id})` }))]
  function save() {
    void form.submit(async draft => {
      const controller = new AbortController()
      active.current.controller = controller
      const timer = setTimeout(() => controller.abort(), 20000)
      try {
        const response = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspace)}/artifact-creation-policy?workspace_id=${encodeURIComponent(workspace)}`, {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          signal: controller.signal,
          body: JSON.stringify({ enabled: draft.enabled, crew_ids: JSON.parse(draft.crewIds), base_version: baseline.version }),
        })
        const body = await response.json().catch(() => null)
        if (controller.signal.aborted) throw new ArtifactPolicyError("Could not confirm policy. Retry the same policy or review the latest.")
        if (!response.ok) throw new ArtifactPolicyError(body?.error ?? "Could not save artifact creation policy.", response.status)
        if (!validArtifactPolicy(body) || body.version !== baseline.version + 1) {
          throw new ArtifactPolicyError("Could not confirm policy. Review the latest before changing it again.")
        }
        setUncertain(false)
        setBaseline(body)
        onSaved(body)
      } catch (error) {
        const failure = error instanceof ArtifactPolicyError ? error : new ArtifactPolicyError("Could not confirm policy. Retry the same policy or review the latest.")
        if (policyAccessDenied(failure)) { onDenied(failure); throw failure }
        setUncertain(failure.status === undefined || failure.status === 408 || (failure.status ?? 0) >= 500)
        throw failure
      } finally {
        clearTimeout(timer)
        active.current.controller = null
      }
    })
  }
  return <>
    <SettingsRow label="Agents may create artifacts" description="Off by default. Enabling this applies only to the crews selected below.">
      <Switch aria-label="Agents may create artifacts" checked={form.draft.enabled} disabled={locked} onCheckedChange={value => form.set("enabled", value)} />
    </SettingsRow>
    <fieldset className="space-y-3 border-t p-4" disabled={locked}>
      <legend className="sr-only">Allowed crews</legend>
      <p className="text-sm font-medium">Allowed crews</p>
      <p className="text-sm text-muted-foreground">This does not grant access to existing Pages or authorize a run. Disabling the policy blocks further agent publications.</p>
      {!options.length && <p className="text-sm text-muted-foreground">Create a crew before enabling artifact creation.</p>}
      {options.map(crew => <label key={crew.id} className="flex min-h-9 items-center gap-3 break-words text-sm coarse:min-h-12">
        <Checkbox aria-label={`Allow artifacts from ${crew.name}`} disabled={locked} checked={ids.includes(crew.id)} onCheckedChange={checked => form.set("crewIds", JSON.stringify((checked ? [...ids, crew.id] : ids.filter(id => id !== crew.id)).sort()))} />
        {crew.name}
      </label>)}
    </fieldset>
    {initial.version !== baseline.version && <p className="px-4 text-sm text-muted-foreground">The saved policy changed. Review latest before saving a new draft.</p>}
    {uncertain && <p className="px-4 text-sm text-muted-foreground">The policy may already have changed. Retry uses the original version; reviewing latest discards this draft.</p>}
    <div className="px-4 pb-3"><Button variant="ghost" className="coarse:h-12" disabled={form.status === "saving"} onClick={() => setConfirmReload(true)}>Review latest policy</Button></div>
    <SaveFooter label="Artifact creation policy" dirty={form.isDirty} status={form.status} error={form.error} onSave={save} onCancel={() => { if (uncertain) setConfirmReload(true); else form.reset() }} saveLabel={uncertain ? "Retry same policy" : "Save policy"} canSave={!form.draft.enabled || ids.length > 0} />
    <AlertDialog open={confirmReload} onOpenChange={setConfirmReload}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Discard draft and review latest policy?</AlertDialogTitle>
          <AlertDialogDescription>This replaces your local choices with the saved workspace policy. An unconfirmed save may already have applied.</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep draft</AlertDialogCancel>
          <AlertDialogAction onClick={onReload}>Discard draft and load latest</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  </>
}
