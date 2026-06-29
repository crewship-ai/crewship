"use client"

import { useState } from "react"
import { Plus, Check, KeyRound, Type, ExternalLink } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import type { Credential } from "../types"
import { isCredentialRef, deriveCredentialName } from "../lib/credential-helpers"
import { OAuthForm } from "./oauth-form"

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface CredentialPickerProps {
  envKey: string
  envValue: string
  credentials: Credential[]
  credLoading: boolean
  workspaceId: string
  onFetchCredentials: () => void
  onAddCredential: (cred: Credential) => void
  onChangeValue: (value: string) => void
}

type PickerMode = "credential" | "manual" | "create" | "oauth"

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function CredentialPicker({
  envKey,
  envValue,
  credentials,
  credLoading,
  workspaceId,
  onFetchCredentials,
  onAddCredential,
  onChangeValue,
}: CredentialPickerProps) {
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<PickerMode>(() => {
    if (!envValue) return "credential"
    if (isCredentialRef(envValue)) return "credential"
    return "manual"
  })
  const [createName, setCreateName] = useState("")
  const [createValue, setCreateValue] = useState("")
  const [creating, setCreating] = useState(false)

  // Derive current credential ref key (e.g. "GITHUB_TOKEN" from "${GITHUB_TOKEN}")
  const currentRefKey = isCredentialRef(envValue)
    ? envValue.slice(2, -1)
    : null

  // Find matching credential
  const selectedCredential = currentRefKey
    ? credentials.find(
        (c) =>
          c.name === deriveCredentialName(currentRefKey) ||
          c.name === currentRefKey ||
          c.name.toLowerCase() === currentRefKey.toLowerCase(),
      )
    : null

  function handleSelectCredential(credName: string) {
    const refKey = envKey.trim() || credName.toUpperCase().replace(/-/g, "_")
    onChangeValue(`\${${refKey}}`)
    setMode("credential")
    setOpen(false)
  }

  function handleSwitchToManual() {
    setMode("manual")
    if (isCredentialRef(envValue)) {
      onChangeValue("")
    }
    setOpen(false)
  }

  function handleSwitchToCreate() {
    setMode("create")
    setCreateName(envKey ? deriveCredentialName(envKey) : "")
    setCreateValue("")
  }

  function handleSwitchToOAuth() {
    setMode("oauth")
  }

  async function handleCreate() {
    if (!createName.trim() || !createValue.trim()) {
      toast.error("Name and value are required")
      return
    }

    setCreating(true)
    try {
      const res = await apiFetch(`/api/v1/credentials?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: createName.trim(),
          type: "SECRET",
          value: createValue.trim(),
          scope: "WORKSPACE",
        }),
      })

      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to create credential" }))
        toast.error(typeof data.error === "string" ? data.error : "Failed to create credential")
        return
      }

      const created: Credential = await res.json()
      onAddCredential(created)
      toast.success(`Credential "${createName.trim()}" created`)

      handleSelectCredential(createName.trim())
      setMode("credential")
      setCreateName("")
      setCreateValue("")
    } catch {
      toast.error("Network error creating credential")
    } finally {
      setCreating(false)
    }
  }

  // ---------------------------------------------------------------------------
  // Manual mode — plain text input
  // ---------------------------------------------------------------------------

  if (mode === "manual" && !open) {
    return (
      <div className="flex items-center gap-1">
        <Input
          value={isCredentialRef(envValue) ? "" : envValue}
          onChange={(ev) => onChangeValue(ev.target.value)}
          placeholder="plain value"
          className="h-7 text-xs font-mono flex-1"
        />
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={() => {
            setMode("credential")
            setOpen(true)
          }}
          className="h-7 w-7 p-0 shrink-0 text-muted-foreground hover:text-foreground"
          title="Switch to credential"
        >
          <KeyRound className="h-3 w-3" />
        </Button>
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Credential / create / oauth mode — picker popover
  // ---------------------------------------------------------------------------

  const triggerLabel = selectedCredential
    ? selectedCredential.name
    : currentRefKey
      ? currentRefKey
      : "Select credential..."

  return (
    <Popover open={open} onOpenChange={(isOpen) => {
      setOpen(isOpen)
      if (isOpen) {
        onFetchCredentials()
      } else {
        if (mode === "oauth" || mode === "create") {
          setMode("credential")
        }
      }
    }}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(
            "flex items-center gap-1.5 w-full h-7 px-2 text-xs rounded-md border",
            "bg-transparent hover:bg-accent/50 transition-colors text-left",
            "border-input",
            !envValue && "text-muted-foreground",
          )}
        >
          {selectedCredential || currentRefKey ? (
            <>
              <span className={cn(
                "h-1.5 w-1.5 rounded-full shrink-0",
                selectedCredential?.status === "ACTIVE" ? "bg-emerald-500" :
                selectedCredential?.status === "PENDING" ? "bg-amber-500" :
                selectedCredential ? "bg-red-500" : "bg-muted-foreground",
              )} />
              <span className="truncate font-mono">{triggerLabel}</span>
              {selectedCredential && (
                <Badge variant="outline" className="ml-auto h-4 text-[10px] px-1 shrink-0">
                  {selectedCredential.type}
                </Badge>
              )}
            </>
          ) : (
            <>
              <KeyRound className="h-3 w-3 shrink-0" />
              <span className="truncate">Select credential...</span>
            </>
          )}
        </button>
      </PopoverTrigger>

      <PopoverContent align="start" className="w-72 p-0">
        {credLoading ? (
          <div className="flex items-center justify-center py-6">
            <Spinner className="h-4 w-4 text-muted-foreground" />
          </div>
        ) : mode === "create" ? (
          <div className="p-3 space-y-3">
            <div className="text-xs font-medium">Create new credential</div>
            <div className="space-y-2">
              <div className="space-y-1">
                <Label className="text-xs text-muted-foreground">Name</Label>
                <Input
                  value={createName}
                  onChange={(ev) => setCreateName(ev.target.value)}
                  placeholder="github-token"
                  className="h-7 text-xs"
                  autoFocus
                />
              </div>
              <div className="space-y-1">
                <Label className="text-xs text-muted-foreground">Secret value</Label>
                <Input
                  type="password"
                  value={createValue}
                  onChange={(ev) => setCreateValue(ev.target.value)}
                  placeholder="ghp_xxxxxxxxxxxx"
                  className="h-7 text-xs font-mono"
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                size="sm"
                className="h-7 text-xs gap-1 flex-1"
                disabled={creating || !createName.trim() || !createValue.trim()}
                onClick={handleCreate}
              >
                {creating && <Spinner className="h-3 w-3" />}
                Save
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 text-xs"
                onClick={() => setMode("credential")}
                disabled={creating}
              >
                Cancel
              </Button>
            </div>
          </div>
        ) : mode === "oauth" ? (
          <OAuthForm
            envKey={envKey}
            workspaceId={workspaceId}
            onAddCredential={onAddCredential}
            onSelectCredential={(credName) => {
              handleSelectCredential(credName)
            }}
            onCancel={() => setMode("credential")}
          />
        ) : (
          <div>
            {credentials.length > 0 && (
              <div className="max-h-48 overflow-y-auto p-1">
                {credentials.map((cred) => {
                  const isSelected =
                    selectedCredential?.id === cred.id ||
                    (currentRefKey &&
                      (cred.name === deriveCredentialName(currentRefKey) ||
                        cred.name === currentRefKey))
                  return (
                    <button
                      key={cred.id}
                      type="button"
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 text-xs rounded-sm",
                        "hover:bg-accent hover:text-accent-foreground transition-colors text-left",
                        isSelected && "bg-accent/50",
                      )}
                      onClick={() => handleSelectCredential(cred.name)}
                    >
                      {isSelected ? (
                        <Check className="h-3 w-3 text-emerald-500 shrink-0" />
                      ) : (
                        <KeyRound className="h-3 w-3 text-muted-foreground shrink-0" />
                      )}
                      <span className="truncate flex-1">{cred.name}</span>
                      {cred.status && cred.status !== "ACTIVE" && (
                        <Badge variant="outline" className={cn(
                          "h-4 text-[10px] px-1 shrink-0",
                          cred.status === "PENDING" && "border-amber-500/50 text-amber-600",
                          cred.status === "EXPIRED" && "border-red-500/50 text-red-600",
                        )}>
                          {cred.status}
                        </Badge>
                      )}
                      <Badge variant="outline" className="h-4 text-[10px] px-1 shrink-0">
                        {cred.type}
                      </Badge>
                    </button>
                  )
                })}
              </div>
            )}

            {credentials.length === 0 && (
              <div className="px-3 py-4 text-xs text-muted-foreground text-center">
                No credentials found
              </div>
            )}

            <div className="border-t p-1 space-y-0.5">
              <button
                type="button"
                className="flex items-center gap-2 w-full px-2 py-1.5 text-xs rounded-sm hover:bg-accent hover:text-accent-foreground transition-colors text-left"
                onClick={handleSwitchToCreate}
              >
                <Plus className="h-3 w-3 shrink-0" />
                <span>Create new credential</span>
              </button>
              <button
                type="button"
                className="flex items-center gap-2 w-full px-2 py-1.5 text-xs rounded-sm hover:bg-accent hover:text-accent-foreground transition-colors text-left"
                onClick={handleSwitchToOAuth}
              >
                <ExternalLink className="h-3 w-3 shrink-0" />
                <span>Connect with OAuth</span>
              </button>
              <button
                type="button"
                className="flex items-center gap-2 w-full px-2 py-1.5 text-xs rounded-sm hover:bg-accent hover:text-accent-foreground transition-colors text-left"
                onClick={handleSwitchToManual}
              >
                <Type className="h-3 w-3 shrink-0" />
                <span>Manual value</span>
              </button>
            </div>
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}
