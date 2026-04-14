"use client"

import { useCallback, useEffect, useState } from "react"
import { Loader2, RefreshCw, CheckCircle2, Circle } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { RuntimeConfig, type RuntimeConfigValue } from "@/components/features/crews/runtime-config"
import { toast } from "sonner"

interface CrewRuntimeConfigProps {
  crewId: string
  workspaceId: string
  runtimeImage: string | null
  devcontainerConfig: string | null
  miseConfig: string | null
  cachedImage: string | null
  canEdit: boolean
  onSave: (config: {
    runtime_image: string | null
    devcontainer_config: string | null
    mise_config: string | null
  }) => Promise<void>
}

export function CrewRuntimeConfig({
  crewId,
  workspaceId,
  runtimeImage,
  devcontainerConfig,
  miseConfig,
  cachedImage,
  canEdit,
  onSave,
}: CrewRuntimeConfigProps) {
  const [value, setValue] = useState<RuntimeConfigValue>({
    runtimeImage: runtimeImage || "",
    devcontainerConfig: devcontainerConfig || "",
    miseConfig: miseConfig || "",
  })
  const [saving, setSaving] = useState(false)
  const [provisioning, setProvisioning] = useState(false)
  const [rebuilding, setRebuilding] = useState(false)

  // Resync from props when they change (e.g. after save)
  useEffect(() => {
    setValue({
      runtimeImage: runtimeImage || "",
      devcontainerConfig: devcontainerConfig || "",
      miseConfig: miseConfig || "",
    })
  }, [runtimeImage, devcontainerConfig, miseConfig])

  const hasChanges =
    value.runtimeImage !== (runtimeImage || "") ||
    value.devcontainerConfig !== (devcontainerConfig || "") ||
    value.miseConfig !== (miseConfig || "")

  const isProvisioned = Boolean(cachedImage)
  const hasConfig = Boolean(devcontainerConfig || miseConfig)

  const handleSave = useCallback(async () => {
    setSaving(true)
    try {
      await onSave({
        runtime_image: value.runtimeImage || null,
        devcontainer_config: value.devcontainerConfig || null,
        mise_config: value.miseConfig || null,
      })
      toast.success("Runtime configuration updated")
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : "Failed to save"
      toast.error(message)
    } finally {
      setSaving(false)
    }
  }, [value, onSave])

  const handleProvision = useCallback(async () => {
    setProvisioning(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/provision?workspace_id=${workspaceId}`,
        { method: "POST" }
      )
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed" }))
        toast.error(data.error || `HTTP ${res.status}`)
        return
      }
      toast.success("Provisioning started. The container will be built on next start.")
    } catch {
      toast.error("Network error")
    } finally {
      setProvisioning(false)
    }
  }, [crewId, workspaceId])

  const handleRebuild = useCallback(async () => {
    setRebuilding(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/rebuild?workspace_id=${workspaceId}`,
        { method: "POST" }
      )
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed" }))
        toast.error(data.error || `HTTP ${res.status}`)
        return
      }
      toast.success("Cache invalidated. Container will be rebuilt on next start.")
    } catch {
      toast.error("Network error")
    } finally {
      setRebuilding(false)
    }
  }, [crewId, workspaceId])

  return (
    <div className="space-y-4">
      {/* Provisioning status */}
      <div className="flex items-center gap-3">
        <Label className="text-xs font-medium">Cache Status</Label>
        {isProvisioned ? (
          <span className="inline-flex items-center gap-1.5 text-xs font-medium text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="h-3.5 w-3.5" />
            Provisioned
          </span>
        ) : (
          <span className="inline-flex items-center gap-1.5 text-xs font-medium text-amber-600 dark:text-amber-400">
            <Circle className="h-3.5 w-3.5" />
            Not provisioned
          </span>
        )}

        {canEdit && hasConfig && (
          <div className="flex gap-1.5 ml-auto">
            {!isProvisioned && (
              <Button
                size="sm"
                variant="outline"
                className="h-7 text-xs"
                onClick={handleProvision}
                disabled={provisioning}
              >
                {provisioning ? (
                  <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                ) : null}
                Provision
              </Button>
            )}
            {isProvisioned && (
              <Button
                size="sm"
                variant="outline"
                className="h-7 text-xs"
                onClick={handleRebuild}
                disabled={rebuilding}
              >
                {rebuilding ? (
                  <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                ) : (
                  <RefreshCw className="mr-1.5 h-3 w-3" />
                )}
                Rebuild
              </Button>
            )}
          </div>
        )}
      </div>

      {/* Runtime config editor */}
      {canEdit ? (
        <>
          <RuntimeConfig value={value} onChange={setValue} />
          {hasChanges && (
            <Button size="sm" onClick={handleSave} disabled={saving}>
              {saving ? "Saving..." : "Save Runtime Config"}
            </Button>
          )}
        </>
      ) : (
        <div className="space-y-2">
          {devcontainerConfig && (
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">Devcontainer Config</Label>
              <pre className="rounded-lg border bg-muted/50 p-3 text-xs font-mono overflow-x-auto whitespace-pre-wrap">
                {devcontainerConfig}
              </pre>
            </div>
          )}
          {miseConfig && (
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">Mise Config</Label>
              <pre className="rounded-lg border bg-muted/50 p-3 text-xs font-mono overflow-x-auto whitespace-pre-wrap">
                {miseConfig}
              </pre>
            </div>
          )}
          {!devcontainerConfig && !miseConfig && (
            <p className="text-xs text-muted-foreground">No runtime configuration set.</p>
          )}
        </div>
      )}
    </div>
  )
}
