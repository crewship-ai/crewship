"use client"

import * as React from "react"
import { Eye, EyeOff, Loader2 } from "lucide-react"
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

interface Team {
  id: string
  name: string
}

interface CredentialData {
  id: string
  name: string
  description: string | null
  scope: "WORKSPACE" | "CREW"
  crew_id: string | null
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
  const [crewId, setTeamId] = React.useState(credential.crew_id ?? "")
  const [showValue, setShowValue] = React.useState(false)
  const [crews, setTeams] = React.useState<Team[]>([])
  const [teamsLoading, setTeamsLoading] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState("")

  React.useEffect(() => {
    setName(credential.name)
    setDescription(credential.description ?? "")
    setScope(credential.scope)
    setTeamId(credential.crew_id ?? "")
    setValue("")
    setShowValue(false)
    setError("")
  }, [credential])

  React.useEffect(() => {
    if (scope === "CREW" && crews.length === 0) {
      setTeamsLoading(true)
      fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        .then((res) => res.json())
        .then((data: Team[]) => setTeams(data))
        .catch(() => setTeams([]))
        .finally(() => setTeamsLoading(false))
    }
  }, [scope, workspaceId, crews.length])

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setError("")
      setValue("")
      setShowValue(false)
    }
    onOpenChange(nextOpen)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError("")

    if (!name.trim()) {
      setError("Name is required")
      return
    }
    if (scope === "CREW" && !crewId) {
      setError("Crew is required for crew-scoped credentials")
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
      if (scope === "CREW") body.crew_id = crewId
      else body.crew_id = null

      const res = await fetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
        method: "PUT",
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
                onChange={(e) => setValue(e.target.value)}
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
          </div>

          <div className="space-y-2">
            <Label htmlFor="edit-cred-scope">Scope</Label>
            <Select value={scope} onValueChange={(v) => { setScope(v as "WORKSPACE" | "CREW"); if (v === "WORKSPACE") setTeamId(""); }}>
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
              <Label htmlFor="edit-cred-team">Crew</Label>
              {teamsLoading ? (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Loading crews...
                </div>
              ) : (
                <Select value={crewId} onValueChange={setTeamId}>
                  <SelectTrigger id="edit-cred-team" className="w-full">
                    <SelectValue placeholder="Select a crew" />
                  </SelectTrigger>
                  <SelectContent>
                    {crews.map((crew) => (
                      <SelectItem key={crew.id} value={crew.id}>
                        {crew.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
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
