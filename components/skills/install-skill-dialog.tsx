"use client"

import { useEffect, useState } from "react"
import { Download } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Label } from "@/components/ui/label"
import { toast } from "sonner"

interface Agent {
  id: string
  name: string
  slug: string
}

interface InstallSkillDialogProps {
  skillId: string
  workspaceId: string
}

export function InstallSkillDialog({ skillId, workspaceId }: InstallSkillDialogProps) {
  const [open, setOpen] = useState(false)
  const [agents, setAgents] = useState<Agent[]>([])
  const [selectedAgentId, setSelectedAgentId] = useState("")
  const [loading, setLoading] = useState(false)
  const [installing, setInstalling] = useState(false)

  useEffect(() => {
    if (!open) return
    setLoading(true)
    fetch(`/api/v1/agents?workspace_id=${workspaceId}`)
      .then((res) => (res.ok ? res.json() : []))
      .then((data: Agent[]) => setAgents(data))
      .catch(() => setAgents([]))
      .finally(() => setLoading(false))
  }, [open, workspaceId])

  async function handleInstall() {
    if (!selectedAgentId) {
      toast.error("Select an agent")
      return
    }

    setInstalling(true)
    try {
      const res = await fetch(
        `/api/v1/agents/${selectedAgentId}/skills?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ skill_id: skillId }),
        }
      )

      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(body?.error ?? "Failed to install skill")
        return
      }

      toast.success("Skill installed to agent")
      setOpen(false)
      setSelectedAgentId("")
    } catch {
      toast.error("Failed to install skill")
    } finally {
      setInstalling(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button className="gap-2">
          <Download className="h-4 w-4" />
          Install to Agent
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Install Skill</DialogTitle>
          <DialogDescription>
            Select an agent to install this skill to.
          </DialogDescription>
        </DialogHeader>
        <div className="py-4">
          <Label htmlFor="agent-select">Agent</Label>
          <Select value={selectedAgentId} onValueChange={setSelectedAgentId}>
            <SelectTrigger className="mt-1">
              <SelectValue placeholder={loading ? "Loading agents..." : "Select an agent"} />
            </SelectTrigger>
            <SelectContent>
              {agents.map((agent) => (
                <SelectItem key={agent.id} value={agent.id}>
                  @{agent.slug} — {agent.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button onClick={handleInstall} disabled={installing || !selectedAgentId}>
            {installing ? "Installing..." : "Install"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
