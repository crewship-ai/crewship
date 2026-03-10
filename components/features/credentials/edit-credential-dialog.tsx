"use client"

import * as React from "react"
import { Eye, EyeOff, Loader2, CheckCircle2, XCircle, FlaskConical, Check, ChevronsUpDown } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

interface Team {
  id: string
  name: string
}

interface CredentialData {
  id: string
  name: string
  description: string | null
  type: string
  provider: string
  scope: "WORKSPACE" | "CREW"
  crew_id: string | null
  crew_ids: string[]
}

interface EditCredentialDialogProps {
  workspaceId: string
  credential: CredentialData
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

export type { CredentialData }

export function EditCredentialDialog({
  workspaceId,
  credential,
  open,
  onOpenChange,
  onSuccess,
}: EditCredentialDialogProps) {
  const [name, setName] = React.useState(credential.name)
  const [description, setDescription] = React.useState(credential.description ?? "")
  const [value, setValue] = React.useState("")
  const [scope, setScope] = React.useState<"WORKSPACE" | "CREW">(credential.scope)
  const [crewIds, setCrewIds] = React.useState<string[]>(credential.crew_ids ?? [])
  const [crewPopoverOpen, setCrewPopoverOpen] = React.useState(false)
  const [showValue, setShowValue] = React.useState(false)
  const [crews, setTeams] = React.useState<Team[]>([])
  const [teamsLoading, setTeamsLoading] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<{ valid: boolean; error?: string } | null>(null)
  const [error, setError] = React.useState("")

  React.useEffect(() => {
    setName(credential.name)
    setDescription(credential.description ?? "")
    setScope(credential.scope)
    setCrewIds(credential.crew_ids ?? [])
    setValue("")
    setShowValue(false)
    setError("")
    setTestResult(null)
  }, [credential])

  React.useEffect(() => {
    if (scope === "CREW" && crews.length === 0) {
      setTeamsLoading(true)
      fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        .then((res) => res.json())
        .then((data: Team[]) => setTeams(Array.isArray(data) ? data : []))
        .catch(() => setTeams([]))
        .finally(() => setTeamsLoading(false))
    }
  }, [scope, workspaceId, crews.length])

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setError("")
      setValue("")
      setShowValue(false)
      setTestResult(null)
      setCrewPopoverOpen(false)
    }
    onOpenChange(nextOpen)
  }

  async function handleTest() {
    if (!value.trim()) {
      setError("Enter a value to test")
      return
    }
    setTesting(true)
    setTestResult(null)
    setError("")
    try {
      const res = await fetch(`/api/v1/credentials/test`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: credential.provider,
          type: credential.type,
          value: value.trim(),
        }),
      })
      if (!res.ok) {
        setTestResult({ valid: false, error: "Test request failed" })
        return
      }
      const data = await res.json()
      setTestResult({ valid: data.valid, error: data.error })
    } catch {
      setTestResult({ valid: false, error: "Network error" })
    } finally {
      setTesting(false)
    }
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError("")

    if (!name.trim()) {
      setError("Name is required")
      return
    }
    if (scope === "CREW" && crewIds.length === 0) {
      setError("At least one crew is required for crew-scoped credentials")
      return
    }

    setSubmitting(true)

    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        scope,
      }
      if (description.trim()) body.description = description.trim()
      else body.description = ""
      if (value) body.value = value
      if (scope === "CREW") body.crew_ids = crewIds
      else body.crew_ids = []

      const res = await fetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })

      if (!res.ok) {
        const data = await res.json()
        setError(typeof data.error === "string" ? data.error : "Failed to update credential")
        return
      }

      handleOpenChange(false)
      onSuccess()
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Edit Credential</DialogTitle>
          <DialogDescription>
            Update the credential details. Leave the value empty to keep the existing secret.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="edit-cred-name">Name</Label>
            <Input
              id="edit-cred-name"
              placeholder="e.g. OPENAI_API_KEY"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="edit-cred-description">Description</Label>
            <Textarea
              id="edit-cred-description"
              placeholder="Optional description..."
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={2}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="edit-cred-value">Value</Label>
            <div className="relative">
              <Input
                id="edit-cred-value"
                type={showValue ? "text" : "password"}
                placeholder="Leave empty to keep existing value"
                value={value}
                onChange={(e) => { setValue(e.target.value); setTestResult(null) }}
                className="pr-10"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="absolute right-2 top-1/2 -translate-y-1/2"
                onClick={() => setShowValue(!showValue)}
              >
                {showValue ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                <span className="sr-only">{showValue ? "Hide" : "Show"} value</span>
              </Button>
            </div>
            {credential.provider !== "NONE" && value.trim() && !value.trim().startsWith("sk-ant-oat") && (
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={handleTest}
                  disabled={testing}
                  className="h-7 text-xs"
                >
                  {testing ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" /> : <FlaskConical className="mr-1.5 h-3 w-3" />}
                  Test Key
                </Button>
                {testResult && (
                  <span className={`flex items-center gap-1 text-xs ${testResult.valid ? "text-green-600 dark:text-green-400" : "text-destructive"}`}>
                    {testResult.valid ? <CheckCircle2 className="h-3.5 w-3.5" /> : <XCircle className="h-3.5 w-3.5" />}
                    {testResult.valid ? "Valid" : testResult.error || "Invalid"}
                  </span>
                )}
              </div>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="edit-cred-scope">Scope</Label>
            <Select value={scope} onValueChange={(v) => { setScope(v as "WORKSPACE" | "CREW"); if (v === "WORKSPACE") setCrewIds([]); }}>
              <SelectTrigger id="edit-cred-scope" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="WORKSPACE">Workspace</SelectItem>
                <SelectItem value="CREW">Crew</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {scope === "CREW" && (
            <div className="space-y-2">
              <Label>Crews</Label>
              {teamsLoading ? (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Loading crews...
                </div>
              ) : (
                <>
                  <Popover open={crewPopoverOpen} onOpenChange={setCrewPopoverOpen}>
                    <PopoverTrigger asChild>
                      <Button
                        variant="outline"
                        role="combobox"
                        aria-expanded={crewPopoverOpen}
                        className="w-full justify-between font-normal"
                      >
                        {crewIds.length === 0
                          ? "Select crews..."
                          : `${crewIds.length} crew${crewIds.length > 1 ? "s" : ""} selected`}
                        <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
                      </Button>
                    </PopoverTrigger>
                    <PopoverContent className="w-[--radix-popover-trigger-width] p-0" align="start">
                      <Command>
                        <CommandInput placeholder="Search crews..." />
                        <CommandList>
                          <CommandEmpty>No crews found.</CommandEmpty>
                          <CommandGroup>
                            {crews.map((crew) => {
                              const isSelected = crewIds.includes(crew.id)
                              return (
                                <CommandItem
                                  key={crew.id}
                                  value={crew.name}
                                  onSelect={() => {
                                    setCrewIds((prev) =>
                                      isSelected
                                        ? prev.filter((id) => id !== crew.id)
                                        : [...prev, crew.id]
                                    )
                                  }}
                                >
                                  <Check className={cn("mr-2 h-4 w-4", isSelected ? "opacity-100" : "opacity-0")} />
                                  {crew.name}
                                </CommandItem>
                              )
                            })}
                          </CommandGroup>
                        </CommandList>
                      </Command>
                    </PopoverContent>
                  </Popover>
                  {crewIds.length > 0 && (
                    <div className="flex flex-wrap gap-1">
                      {crewIds.map((id) => {
                        const crew = crews.find((c) => c.id === id)
                        return crew ? (
                          <Badge
                            key={id}
                            variant="secondary"
                            className="cursor-pointer"
                            onClick={() => setCrewIds((prev) => prev.filter((cid) => cid !== id))}
                          >
                            {crew.name}
                            <XCircle className="ml-1 h-3 w-3" />
                          </Badge>
                        ) : null
                      })}
                    </div>
                  )}
                </>
              )}
            </div>
          )}

          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Save Changes
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
